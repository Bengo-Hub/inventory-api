package items

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

type ItemDTO struct {
	ID              uuid.UUID      `json:"id"`
	SKU             string         `json:"sku"`
	Name            string         `json:"name"`
	Description     string         `json:"description,omitempty"`
	CategoryID      *uuid.UUID     `json:"category_id,omitempty"`
	UnitID          *uuid.UUID     `json:"unit_id,omitempty"`
	Type            string         `json:"type"` // GOODS | SERVICE | RECIPE | INGREDIENT
	IsActive        bool           `json:"is_active"`
	ImageURL        string         `json:"image_url,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	InitialQuantity int            `json:"initial_quantity,omitempty"`
	ReorderLevel    int            `json:"reorder_level,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type CategoryDTO struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Code        string    `json:"code,omitempty"`
	Description string    `json:"description,omitempty"`
	IsActive    bool      `json:"is_active"`
}

// StockAvailability matches the DTO expected by the ordering-backend client.
type StockAvailability struct {
	ItemID        uuid.UUID `json:"item_id"`
	SKU           string    `json:"sku"`
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	OnHand        int       `json:"on_hand"`
	Available     int       `json:"available"`
	Reserved      int       `json:"reserved"`
	UnitOfMeasure string    `json:"unit_of_measure"`
	UpdatedAt     string    `json:"updated_at"`
}

// Service handles item-related business logic.
type Service struct {
	client *ent.Client
	cache  *sharedcache.Aside
	log    *zap.Logger
}

// NewService creates a new items service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("items.service"),
	}
}

// SetCache injects the cache helper (optional; caching is skipped if nil).
func (s *Service) SetCache(c *sharedcache.Aside) {
	s.cache = c
}

// GetStockAvailability returns stock availability for a single item by SKU.
// If the item type is RECIPE, it resolves the BOM and returns the minimum
// available portions based on ingredient stock levels (BOM explosion).
func (s *Service) GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*StockAvailability, error) {
	itm, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.Sku(sku),
			item.IsActive(true),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: item not found: sku=%s", sku)
		}
		return nil, fmt.Errorf("items: query item: %w", err)
	}

	// BOM explosion: if item type is RECIPE, compute available portions from ingredients
	if itm.Type == item.TypeRECIPE {
		return s.getRecipeAvailability(ctx, tenantID, itm)
	}

	return s.getDirectAvailability(ctx, tenantID, itm)
}

// getDirectAvailability returns availability for a non-recipe item directly from InventoryBalance.
func (s *Service) getDirectAvailability(ctx context.Context, tenantID uuid.UUID, itm *ent.Item) (*StockAvailability, error) {
	bal, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itm.ID),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return &StockAvailability{
				ItemID:        itm.ID,
				SKU:           itm.Sku,
				WarehouseID:   uuid.Nil,
				OnHand:        0,
				Available:     0,
				Reserved:      0,
				UnitOfMeasure: "",
				UpdatedAt:     itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			}, nil
		}
		return nil, fmt.Errorf("items: query balance: %w", err)
	}

	return &StockAvailability{
		ItemID:        itm.ID,
		SKU:           itm.Sku,
		WarehouseID:   bal.WarehouseID,
		OnHand:        bal.OnHand,
		Available:     bal.Available,
		Reserved:      bal.Reserved,
		UnitOfMeasure: bal.UnitOfMeasure,
		UpdatedAt:     bal.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

// getRecipeAvailability performs BOM explosion: for a RECIPE item, looks up the recipe,
// checks each ingredient's available balance, and returns the minimum number of portions
// that can be produced (floor(ingredient_available / ingredient_qty_per_portion)).
func (s *Service) getRecipeAvailability(ctx context.Context, tenantID uuid.UUID, itm *ent.Item) (*StockAvailability, error) {
	rec, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(itm.Sku), recipe.IsActive(true)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
		}).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// No BOM defined — fall back to direct balance check
			return s.getDirectAvailability(ctx, tenantID, itm)
		}
		return nil, fmt.Errorf("items: lookup recipe for sku=%s: %w", itm.Sku, err)
	}

	if len(rec.Edges.Ingredients) == 0 {
		return s.getDirectAvailability(ctx, tenantID, itm)
	}

	// Collect ingredient item IDs
	ingredientIDs := make([]uuid.UUID, len(rec.Edges.Ingredients))
	for i, ing := range rec.Edges.Ingredients {
		ingredientIDs[i] = ing.ItemID
	}

	// Fetch all ingredient balances in one query
	balances, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemIDIn(ingredientIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query ingredient balances: %w", err)
	}

	balMap := make(map[uuid.UUID]int, len(balances))
	for _, b := range balances {
		balMap[b.ItemID] = b.Available
	}

	// BOM explosion: compute minimum available portions
	outputQty := rec.OutputQty
	if outputQty <= 0 {
		outputQty = 1
	}

	minPortions := math.MaxFloat64
	for _, ing := range rec.Edges.Ingredients {
		available := float64(balMap[ing.ItemID])
		qtyPerPortion := ing.Quantity / outputQty
		if qtyPerPortion <= 0 {
			continue
		}
		portions := available / qtyPerPortion
		if portions < minPortions {
			minPortions = portions
		}
	}

	if minPortions == math.MaxFloat64 {
		minPortions = 0
	}

	availablePortions := int(math.Floor(minPortions))

	return &StockAvailability{
		ItemID:        itm.ID,
		SKU:           itm.Sku,
		WarehouseID:   uuid.Nil,
		OnHand:        availablePortions,
		Available:     availablePortions,
		Reserved:      0,
		UnitOfMeasure: rec.UnitOfMeasure,
		UpdatedAt:     itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

// BulkAvailability returns stock availability for multiple items by SKU.
func (s *Service) BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]StockAvailability, error) {
	items, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.SkuIn(skus...),
			item.IsActive(true),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query items: %w", err)
	}

	itemIDs := make([]uuid.UUID, len(items))
	itemMap := make(map[uuid.UUID]*ent.Item, len(items))
	for i, itm := range items {
		itemIDs[i] = itm.ID
		itemMap[itm.ID] = itm
	}

	balances, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemIDIn(itemIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query balances: %w", err)
	}

	balMap := make(map[uuid.UUID]*ent.InventoryBalance, len(balances))
	for _, b := range balances {
		balMap[b.ItemID] = b
	}

	result := make([]StockAvailability, 0, len(items))
	for _, itm := range items {
		avail := StockAvailability{
			ItemID:    itm.ID,
			SKU:       itm.Sku,
			UpdatedAt: itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if bal, ok := balMap[itm.ID]; ok {
			avail.WarehouseID = bal.WarehouseID
			avail.OnHand = bal.OnHand
			avail.Available = bal.Available
			avail.Reserved = bal.Reserved
			avail.UpdatedAt = bal.UpdatedAt.Format("2006-01-02T15:04:05Z")
		}
		result = append(result, avail)
	}

	return result, nil
}
// InventorySummary represents high-level stock metrics.
type InventorySummary struct {
	TotalItems       int `json:"total_items"`
	LowStockItems    int `json:"low_stock_items"`
	OutOfStockItems  int `json:"out_of_stock_items"`
}

// GetInventorySummary returns aggregated stock metrics for a tenant.
func (s *Service) GetInventorySummary(ctx context.Context, tenantID uuid.UUID) (*InventorySummary, error) {
	total, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.IsActive(true)).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: count total items: %w", err)
	}

	// Assuming 10 is the default low stock threshold if not specified on item
	lowStock, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.AvailableLTE(10), // Simplification: threshold = 10
			inventorybalance.AvailableGT(0),
		).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: count low stock: %w", err)
	}

	outOfStock, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.AvailableLTE(0),
		).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: count out of stock: %w", err)
	}

	return &InventorySummary{
		TotalItems:      total,
		LowStockItems:   lowStock,
		OutOfStockItems: outOfStock,
	}, nil
}

func (s *Service) mapToDTO(i *ent.Item) *ItemDTO {
	return &ItemDTO{
		ID:          i.ID,
		SKU:         i.Sku,
		Name:        i.Name,
		Description: i.Description,
		CategoryID:  i.CategoryID,
		UnitID:      i.UnitID,
		Type:        string(i.Type),
		IsActive:    i.IsActive,
		ImageURL:    i.ImageURL,
		Metadata:    i.Metadata,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   i.UpdatedAt,
	}
}

// ListItems returns all active items for a tenant (cached 1 min).
func (s *Service) ListItems(ctx context.Context, tenantID uuid.UUID) ([]ItemDTO, error) {
	key := sharedcache.Key("inv", "items", tenantID.String())
	fetch := func(ctx context.Context) ([]ItemDTO, error) {
		itms, err := s.client.Item.Query().
			Where(item.TenantID(tenantID), item.IsActive(true)).
			Order(ent.Asc(item.FieldSku)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("items: list: %w", err)
		}
		dtos := make([]ItemDTO, len(itms))
		for i, it := range itms {
			dtos[i] = *s.mapToDTO(it)
		}
		return dtos, nil
	}
	return sharedcache.GetOrSet(ctx, s.cache, key, sharedcache.TTLModerate, fetch)
}

// ListCategories returns all item categories for a tenant (cached 5 min).
func (s *Service) ListCategories(ctx context.Context, tenantID uuid.UUID) ([]CategoryDTO, error) {
	key := sharedcache.Key("inv", "categories", tenantID.String())
	fetch := func(ctx context.Context) ([]CategoryDTO, error) {
		cats, err := s.client.ItemCategory.Query().
			Where(itemcategory.TenantID(tenantID), itemcategory.IsActive(true)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("items: list categories: %w", err)
		}
		dtos := make([]CategoryDTO, len(cats))
		for i, c := range cats {
			dtos[i] = CategoryDTO{
				ID:          c.ID,
				Name:        c.Name,
				Code:        c.Code,
				Description: c.Description,
				IsActive:    c.IsActive,
			}
		}
		return dtos, nil
	}
	return sharedcache.GetOrSet(ctx, s.cache, key, sharedcache.TTLReference, fetch)
}

// itemTypeCode maps item types to short codes for SKU generation.
var itemTypeCode = map[string]string{
	"GOODS":      "GDS",
	"SERVICE":    "SVC",
	"RECIPE":     "RCP",
	"INGREDIENT": "ING",
	"VOUCHER":    "VCH",
	"EQUIPMENT":  "EQP",
}

// GenerateSKU creates a unique SKU in the format {CAT_CODE}-{TYPE_CODE}-{SEQ:03d}.
func (s *Service) GenerateSKU(ctx context.Context, tenantID uuid.UUID, categoryID *uuid.UUID, itemType string) (string, error) {
	catCode := "GEN"
	if categoryID != nil {
		cat, err := s.client.ItemCategory.Get(ctx, *categoryID)
		if err == nil && cat.Code != "" {
			catCode = strings.ToUpper(cat.Code)
		} else if err == nil {
			// Derive code from first 3 chars of name
			name := strings.ToUpper(strings.ReplaceAll(cat.Name, " ", ""))
			if len(name) >= 3 {
				catCode = name[:3]
			} else {
				catCode = name
			}
		}
	}

	typeCode, ok := itemTypeCode[strings.ToUpper(itemType)]
	if !ok {
		typeCode = "GDS"
	}

	prefix := catCode + "-" + typeCode + "-"

	// Count existing items with this prefix to determine next sequence
	count, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.SkuHasPrefix(prefix),
		).
		Count(ctx)
	if err != nil {
		return "", fmt.Errorf("items: count items for SKU prefix %s: %w", prefix, err)
	}

	return fmt.Sprintf("%s%03d", prefix, count+1), nil
}

// CreateItem creates a new item and records an outbox event within a transaction.
func (s *Service) CreateItem(ctx context.Context, tenantID uuid.UUID, dto ItemDTO) (*ItemDTO, error) {
	// Auto-generate SKU if not provided
	if dto.SKU == "" {
		sku, err := s.GenerateSKU(ctx, tenantID, dto.CategoryID, dto.Type)
		if err != nil {
			return nil, fmt.Errorf("items: auto-generate SKU: %w", err)
		}
		dto.SKU = sku
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	i, err := tx.Item.Create().
		SetTenantID(tenantID).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetNillableDescription(&dto.Description).
		SetNillableCategoryID(dto.CategoryID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetMetadata(dto.Metadata).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create item: %w", err)
	}

	// Create initial balance if initial_quantity > 0
	initialQty := dto.InitialQuantity
	if initialQty <= 0 {
		initialQty = 1
	}
	reorderLevel := dto.ReorderLevel
	if reorderLevel <= 0 {
		reorderLevel = 1
	}

	// Resolve default warehouse
	wh, whErr := s.client.Warehouse.Query().
		Where(
			warehouse.TenantID(tenantID),
			warehouse.IsDefault(true),
			warehouse.IsActive(true),
		).
		First(ctx)
	if whErr == nil {
		// Resolve unit of measure name for the balance
		uom := "PIECE"
		if dto.UnitID != nil {
			u, uErr := s.client.Unit.Get(ctx, *dto.UnitID)
			if uErr == nil {
				uom = u.Name
			}
		}

		_, err = tx.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(i.ID).
			SetWarehouseID(wh.ID).
			SetOnHand(initialQty).
			SetAvailable(initialQty).
			SetReserved(0).
			SetReorderLevel(reorderLevel).
			SetUnitOfMeasure(uom).
			Save(ctx)
		if err != nil {
			s.log.Warn("items: create initial balance failed", zap.Error(err), zap.String("sku", dto.SKU))
		}
	}

	// Resolve category name for enriched event payload
	categoryName := ""
	if dto.CategoryID != nil {
		cat, catErr := s.client.ItemCategory.Get(ctx, *dto.CategoryID)
		if catErr == nil {
			categoryName = cat.Name
		}
	}

	// Resolve unit name for enriched event payload
	unitName := ""
	if dto.UnitID != nil {
		u, uErr := s.client.Unit.Get(ctx, *dto.UnitID)
		if uErr == nil {
			unitName = u.Name
		}
	}

	// Publish enriched event to outbox
	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item",
		AggregateID:   i.ID,
		EventType:     "inventory.item.created",
		Payload: map[string]any{
			"id":            i.ID,
			"sku":           i.Sku,
			"name":          i.Name,
			"description":   i.Description,
			"type":          i.Type,
			"category_id":   i.CategoryID,
			"category_name": categoryName,
			"unit_id":       i.UnitID,
			"unit_name":     unitName,
			"is_active":     i.IsActive,
			"image_url":     i.ImageURL,
		},
		Timestamp: time.Now().UTC(),
	}

	payload, err := event.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("items: marshal event: %w", err)
	}

	_, err = tx.OutboxEvent.Create().
		SetID(event.ID).
		SetTenantID(tenantID).
		SetAggregateType(event.AggregateType).
		SetAggregateID(event.AggregateID.String()).
		SetEventType(event.EventType).
		SetPayload(payload).
		SetStatus("PENDING").
		SetCreatedAt(event.Timestamp).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create outbox record: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit transaction: %w", err)
	}

	return s.mapToDTO(i), nil
}

// UpdateItem updates an item and records an outbox event within a transaction.
func (s *Service) UpdateItem(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, dto ItemDTO) (*ItemDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	builder := tx.Item.UpdateOneID(id).
		Where(item.TenantID(tenantID)).
		SetName(dto.Name).
		SetNillableDescription(&dto.Description).
		SetNillableCategoryID(dto.CategoryID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetMetadata(dto.Metadata)

	i, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: update item: %w", err)
	}

	// Resolve category name for enriched event payload
	categoryName := ""
	if i.CategoryID != nil {
		cat, catErr := s.client.ItemCategory.Get(ctx, *i.CategoryID)
		if catErr == nil {
			categoryName = cat.Name
		}
	}

	// Resolve unit name for enriched event payload
	unitName := ""
	if i.UnitID != nil {
		u, uErr := s.client.Unit.Get(ctx, *i.UnitID)
		if uErr == nil {
			unitName = u.Name
		}
	}

	// Publish enriched event to outbox
	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item",
		AggregateID:   i.ID,
		EventType:     "inventory.item.updated",
		Payload: map[string]any{
			"id":            i.ID,
			"sku":           i.Sku,
			"name":          i.Name,
			"description":   i.Description,
			"type":          i.Type,
			"category_id":   i.CategoryID,
			"category_name": categoryName,
			"unit_id":       i.UnitID,
			"unit_name":     unitName,
			"is_active":     i.IsActive,
			"image_url":     i.ImageURL,
		},
		Timestamp: time.Now().UTC(),
	}

	payload, err := event.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("items: marshal event: %w", err)
	}

	_, err = tx.OutboxEvent.Create().
		SetID(event.ID).
		SetTenantID(tenantID).
		SetAggregateType(event.AggregateType).
		SetAggregateID(event.AggregateID.String()).
		SetEventType(event.EventType).
		SetPayload(payload).
		SetStatus("PENDING").
		SetCreatedAt(event.Timestamp).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create outbox record: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit transaction: %w", err)
	}

	return s.mapToDTO(i), nil
}
