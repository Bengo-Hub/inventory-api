package items

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
)

type ItemDTO struct {
	ID          uuid.UUID      `json:"id"`
	SKU         string         `json:"sku"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	CategoryID  *uuid.UUID     `json:"category_id,omitempty"`
	UnitID      *uuid.UUID     `json:"unit_id,omitempty"`
	Type        string         `json:"type"` // GOODS | SERVICE | RECIPE | INGREDIENT
	IsActive    bool           `json:"is_active"`
	ImageURL    string         `json:"image_url,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type CategoryDTO struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
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
	log    *zap.Logger
}

// NewService creates a new items service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("items.service"),
	}
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

// ListItems returns all active items for a tenant.
func (s *Service) ListItems(ctx context.Context, tenantID uuid.UUID) ([]ItemDTO, error) {
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

// ListCategories returns all item categories for a tenant.
func (s *Service) ListCategories(ctx context.Context, tenantID uuid.UUID) ([]CategoryDTO, error) {
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
			Description: c.Description,
			IsActive:    c.IsActive,
		}
	}
	return dtos, nil
}

// CreateItem creates a new item and records an outbox event within a transaction.
func (s *Service) CreateItem(ctx context.Context, tenantID uuid.UUID, dto ItemDTO) (*ItemDTO, error) {
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

	// Publish event to outbox
	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item",
		AggregateID:   i.ID,
		EventType:     "inventory.item.created",
		Payload: map[string]any{
			"id":           i.ID,
			"sku":          i.Sku,
			"name":         i.Name,
			"type":         i.Type,
			"category_id":  i.CategoryID,
			"unit_id":      i.UnitID,
			"is_active":    i.IsActive,
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

	// Publish event to outbox
	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item",
		AggregateID:   i.ID,
		EventType:     "inventory.item.updated",
		Payload: map[string]any{
			"id":           i.ID,
			"sku":          i.Sku,
			"name":         i.Name,
			"type":         i.Type,
			"category_id":  i.CategoryID,
			"unit_id":      i.UnitID,
			"is_active":    i.IsActive,
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
