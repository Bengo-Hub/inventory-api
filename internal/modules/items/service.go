package items

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	entdialect "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/reservation"
	"github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// StandardTags defines well-known dietary and allergen tag values.
var StandardTags = []string{
	"vegan", "vegetarian", "gluten_free", "dairy_free", "nut_free",
	"halal", "kosher", "organic", "spicy", "contains_nuts",
	"contains_dairy", "contains_gluten", "sugar_free", "low_cal",
}

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
	Tags            []string       `json:"tags,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	InitialQuantity int            `json:"initial_quantity,omitempty"`
	ReorderLevel    int            `json:"reorder_level"`
	ReorderQuantity int            `json:"reorder_quantity"`
	SuggestedPrice  *float64       `json:"suggested_price,omitempty"`
	AddToAllOutlets bool           `json:"add_to_all_outlets,omitempty"`
	CategoryName    string         `json:"category_name,omitempty"`
	// Extended fields for POS, logistics, compliance
	Barcode                string             `json:"barcode,omitempty"`
	BarcodeType            string             `json:"barcode_type,omitempty"`
	RequiresAgeVerification bool              `json:"requires_age_verification"`
	IsPerishable           bool               `json:"is_perishable"`
	TrackLots              bool               `json:"track_lots"`
	TrackSerialNumbers     bool               `json:"track_serial_numbers"`
	WeightKg               *float64           `json:"weight_kg,omitempty"`
	DimensionsCm           map[string]float64 `json:"dimensions_cm,omitempty"`
	// Cost / pricing fields
	CostPrice *float64 `json:"cost_price,omitempty"`
	// KRA eTIMS tax fields
	TaxCodeID    string `json:"tax_code_id,omitempty"`
	TaxInclusive bool   `json:"tax_inclusive"`
	// Event capacity fields — SERVICE type only
	TotalCapacity  *int       `json:"total_capacity,omitempty"`
	BookedCapacity *int       `json:"booked_capacity,omitempty"`
	EventStartAt   *time.Time `json:"event_start_at,omitempty"`
	EventEndAt     *time.Time `json:"event_end_at,omitempty"`
	EventVenue     *string    `json:"event_venue,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type CategoryDTO struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Code        string     `json:"code,omitempty"`
	Description string     `json:"description,omitempty"`
	Icon        string     `json:"icon,omitempty"`
	ParentID    *uuid.UUID `json:"parent_id,omitempty"`
	ParentName  string     `json:"parent_name,omitempty"`
	IsActive    bool       `json:"is_active"`
}

// StockAvailability matches the DTO expected by the ordering-backend client.
type StockAvailability struct {
	ItemID              uuid.UUID  `json:"item_id"`
	SKU                 string     `json:"sku"`
	WarehouseID         uuid.UUID  `json:"warehouse_id"`
	OnHand              int        `json:"on_hand"`
	Available           int        `json:"available"`
	Reserved            int        `json:"reserved"`
	UnitOfMeasure       string     `json:"unit_of_measure"`
	ReorderLevel        int        `json:"reorder_level"`
	ReorderQuantity     int        `json:"reorder_quantity"`
	PreferredSupplierID *uuid.UUID `json:"preferred_supplier_id,omitempty"`
	UpdatedAt           string     `json:"updated_at"`
}

// Service handles item-related business logic.
type Service struct {
	client       *ent.Client
	cache        *sharedcache.Aside
	log          *zap.Logger
	mediaURLBase string // public base URL for resolving relative /media/ paths
}

// NewService creates a new items service.
func NewService(client *ent.Client, log *zap.Logger, mediaURLBase string) *Service {
	return &Service{
		client:       client,
		mediaURLBase: strings.TrimRight(mediaURLBase, "/"),
		log:          log.Named("items.service"),
	}
}

// resolveMediaURL converts a relative /media/ path to a full URL using MEDIA_URL_BASE.
// Also encodes spaces in filenames to ensure valid URLs.
func (s *Service) resolveMediaURL(path string) string {
	if path == "" || strings.HasPrefix(path, "http") {
		// Even full URLs may have unencoded spaces from legacy data
		return strings.ReplaceAll(path, " ", "%20")
	}
	path = strings.ReplaceAll(path, " ", "%20")
	if s.mediaURLBase != "" {
		return s.mediaURLBase + path
	}
	return path
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
		ItemID:              itm.ID,
		SKU:                 itm.Sku,
		WarehouseID:         bal.WarehouseID,
		OnHand:              bal.OnHand,
		Available:           bal.Available,
		Reserved:            bal.Reserved,
		UnitOfMeasure:       bal.UnitOfMeasure,
		ReorderLevel:        bal.ReorderLevel,
		ReorderQuantity:     bal.ReorderQuantity,
		PreferredSupplierID: bal.PreferredSupplierID,
		UpdatedAt:           bal.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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

// BOMAvailabilityResult represents the BOM-aware availability for a single SKU.
type BOMAvailabilityResult struct {
	SKU           string    `json:"sku"`
	ItemID        uuid.UUID `json:"item_id"`
	Available     int       `json:"available"`
	Type          string    `json:"type"` // "recipe" or "simple"
	UnitOfMeasure string    `json:"unit_of_measure,omitempty"`
	UpdatedAt     string    `json:"updated_at"`
}

// GetBOMAvailability returns BOM-aware availability for multiple SKUs.
// For RECIPE items, it computes the maximum portions producible from ingredient stock.
// For non-recipe items, it returns direct stock availability.
func (s *Service) GetBOMAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]BOMAvailabilityResult, error) {
	results := make([]BOMAvailabilityResult, 0, len(skus))
	for _, sku := range skus {
		itm, err := s.client.Item.Query().
			Where(
				item.TenantID(tenantID),
				item.Sku(sku),
				item.IsActive(true),
			).
			Only(ctx)
		if err != nil {
			s.log.Warn("bom availability: item not found", zap.String("sku", sku), zap.Error(err))
			continue
		}

		if itm.Type == item.TypeRECIPE {
			avail, err := s.getRecipeAvailability(ctx, tenantID, itm)
			if err != nil {
				s.log.Warn("bom availability: recipe check failed", zap.String("sku", sku), zap.Error(err))
				// Fall back to simple
				avail, err = s.getDirectAvailability(ctx, tenantID, itm)
				if err != nil {
					continue
				}
				results = append(results, BOMAvailabilityResult{
					SKU:           sku,
					ItemID:        itm.ID,
					Available:     avail.Available,
					Type:          "simple",
					UnitOfMeasure: avail.UnitOfMeasure,
					UpdatedAt:     avail.UpdatedAt,
				})
				continue
			}
			results = append(results, BOMAvailabilityResult{
				SKU:           sku,
				ItemID:        itm.ID,
				Available:     avail.Available,
				Type:          "recipe",
				UnitOfMeasure: avail.UnitOfMeasure,
				UpdatedAt:     avail.UpdatedAt,
			})
		} else {
			avail, err := s.getDirectAvailability(ctx, tenantID, itm)
			if err != nil {
				s.log.Warn("bom availability: direct check failed", zap.String("sku", sku), zap.Error(err))
				continue
			}
			results = append(results, BOMAvailabilityResult{
				SKU:           sku,
				ItemID:        itm.ID,
				Available:     avail.Available,
				Type:          "simple",
				UnitOfMeasure: avail.UnitOfMeasure,
				UpdatedAt:     avail.UpdatedAt,
			})
		}
	}
	return results, nil
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
	TotalItems          int `json:"total_items"`
	LowStockItems       int `json:"low_stock_items"`
	OutOfStockItems     int `json:"out_of_stock_items"`
	PendingReservations int `json:"pending_reservations"`
	WarehouseCount      int `json:"warehouse_count"`
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

	pendingReservations, err := s.client.Reservation.Query().
		Where(reservation.TenantID(tenantID), reservation.StatusEQ("pending")).
		Count(ctx)
	if err != nil {
		pendingReservations = 0 // non-fatal
	}

	warehouseCount, err := s.client.Warehouse.Query().
		Where(warehouse.TenantID(tenantID), warehouse.IsActive(true)).
		Count(ctx)
	if err != nil {
		warehouseCount = 0 // non-fatal
	}

	return &InventorySummary{
		TotalItems:          total,
		LowStockItems:       lowStock,
		OutOfStockItems:     outOfStock,
		PendingReservations: pendingReservations,
		WarehouseCount:      warehouseCount,
	}, nil
}

func (s *Service) mapToDTO(i *ent.Item) *ItemDTO {
	return &ItemDTO{
		ID:                      i.ID,
		SKU:                     i.Sku,
		Name:                    i.Name,
		Description:             i.Description,
		CategoryID:              i.CategoryID,
		UnitID:                  i.UnitID,
		Type:                    string(i.Type),
		IsActive:                i.IsActive,
		ImageURL:                s.resolveMediaURL(i.ImageURL),
		Tags:                    i.Tags,
		Metadata:                i.Metadata,
		Barcode:                 i.Barcode,
		BarcodeType:             i.BarcodeType,
		RequiresAgeVerification: i.RequiresAgeVerification,
		IsPerishable:            i.IsPerishable,
		TrackLots:               i.TrackLots,
		TrackSerialNumbers:      i.TrackSerialNumbers,
		WeightKg:                i.WeightKg,
		DimensionsCm:            i.DimensionsCm,
		CostPrice:               i.CostPrice,
		TaxCodeID:               i.TaxCodeID,
		TaxInclusive:            i.TaxInclusive,
		TotalCapacity:           i.TotalCapacity,
		BookedCapacity:          i.BookedCapacity,
		EventStartAt:            i.EventStartAt,
		EventEndAt:              i.EventEndAt,
		EventVenue:              i.EventVenue,
		CreatedAt:               i.CreatedAt,
		UpdatedAt:               i.UpdatedAt,
	}
}

// ListItems returns a paginated list of items for a tenant with DB-level filtering.
// statusFilter: "" or "active" = active only (default), "inactive" = inactive only, "all" = both.
// outletID: when set, restricts items to those with a balance in the outlet's warehouses or shared warehouses (outlet_id IS NULL).
func (s *Service) ListItems(ctx context.Context, tenantID uuid.UUID, typeFilter, statusFilter string, limit, offset int, categoryID *uuid.UUID, unitID *uuid.UUID, search string, outletID *uuid.UUID, tagsFilter ...string) ([]ItemDTO, int, error) {
	// Pre-compute outlet-scoped item IDs when outlet context is active.
	var outletItemIDs []uuid.UUID
	if outletID != nil {
		wIDs, _ := s.client.Warehouse.Query().
			Where(
				warehouse.TenantID(tenantID),
				warehouse.Or(
					warehouse.OutletIDEQ(*outletID),
					warehouse.OutletIDIsNil(),
				),
			).IDs(ctx)
		if len(wIDs) == 0 {
			return []ItemDTO{}, 0, nil
		}
		bals, _ := s.client.InventoryBalance.Query().
			Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.WarehouseIDIn(wIDs...)).
			All(ctx)
		idSet := make(map[uuid.UUID]struct{}, len(bals))
		for _, b := range bals {
			idSet[b.ItemID] = struct{}{}
		}
		if len(idSet) == 0 {
			return []ItemDTO{}, 0, nil
		}
		outletItemIDs = make([]uuid.UUID, 0, len(idSet))
		for id := range idSet {
			outletItemIDs = append(outletItemIDs, id)
		}
	}

	buildQuery := func() *ent.ItemQuery {
		q := s.client.Item.Query().Where(item.TenantID(tenantID))
		switch statusFilter {
		case "inactive":
			q = q.Where(item.IsActive(false))
		case "all":
			// no is_active filter
		default:
			q = q.Where(item.IsActive(true))
		}
		if typeFilter != "" {
			types := strings.Split(typeFilter, ",")
			if len(types) == 1 {
				q = q.Where(item.TypeEQ(item.Type(strings.TrimSpace(types[0]))))
			} else {
				typeVals := make([]item.Type, 0, len(types))
				for _, t := range types {
					typeVals = append(typeVals, item.Type(strings.TrimSpace(t)))
				}
				q = q.Where(item.TypeIn(typeVals...))
			}
		}
		if categoryID != nil {
			q = q.Where(item.CategoryID(*categoryID))
		}
		if unitID != nil {
			q = q.Where(item.UnitID(*unitID))
		}
		if search != "" {
			q = q.Where(item.Or(
				item.NameContainsFold(search),
				item.SkuContainsFold(search),
			))
		}
		// Tag filtering via JSONB containment — each tag is ANDed at DB level.
		for _, tag := range tagsFilter {
			tagVal := tag
			q = q.Where(predicate.Item(func(s *entdialect.Selector) {
				s.Where(sqljson.ValueContains(item.FieldTags, tagVal))
			}))
		}
		// Outlet scope: restrict to items with balances in this outlet's warehouses.
		if outletItemIDs != nil {
			q = q.Where(item.IDIn(outletItemIDs...))
		}
		return q
	}

	buildDTOs := func(innerCtx context.Context, itms []*ent.Item) ([]ItemDTO, error) {
		catIDs := make([]uuid.UUID, 0, len(itms))
		itemIDs := make([]uuid.UUID, 0, len(itms))
		for _, it := range itms {
			if it.CategoryID != nil {
				catIDs = append(catIDs, *it.CategoryID)
			}
			itemIDs = append(itemIDs, it.ID)
		}
		catNames := make(map[uuid.UUID]string)
		if len(catIDs) > 0 {
			cats, _ := s.client.ItemCategory.Query().Where(itemcategory.IDIn(catIDs...)).All(innerCtx)
			for _, c := range cats {
				catNames[c.ID] = c.Name
			}
		}
		// Load first balance per item to surface reorder_level and reorder_quantity.
		type balSummary struct{ reorderLevel, reorderQuantity int }
		balMap := make(map[uuid.UUID]balSummary, len(itemIDs))
		if len(itemIDs) > 0 {
			bals, _ := s.client.InventoryBalance.Query().
				Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.ItemIDIn(itemIDs...)).
				All(innerCtx)
			for _, b := range bals {
				if _, seen := balMap[b.ItemID]; !seen {
					balMap[b.ItemID] = balSummary{b.ReorderLevel, b.ReorderQuantity}
				}
			}
		}
		// Load tenant config once for suggested price computation.
		cfg, _ := s.client.TenantInventoryConfig.Query().
			Where(tenantinventoryconfig.TenantID(tenantID)).
			Only(innerCtx)
		dtos := make([]ItemDTO, len(itms))
		for i, it := range itms {
			dto := s.mapToDTO(it)
			if it.CategoryID != nil {
				dto.CategoryName = catNames[*it.CategoryID]
			}
			if bs, ok := balMap[it.ID]; ok {
				dto.ReorderLevel = bs.reorderLevel
				dto.ReorderQuantity = bs.reorderQuantity
			}
			if cfg != nil && it.CostPrice != nil && *it.CostPrice > 0 && cfg.DefaultTargetMarginPercent != nil {
				m := *cfg.DefaultTargetMarginPercent
				if m > 0 && m < 100 {
					sp := *it.CostPrice / (1 - m/100)
					dto.SuggestedPrice = &sp
				}
			}
			dtos[i] = *dto
		}
		return dtos, nil
	}

	// DB-level pagination for all queries (including tag-filtered).
	total, err := buildQuery().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: count: %w", err)
	}
	if total == 0 {
		return []ItemDTO{}, 0, nil
	}
	itms, err := buildQuery().Order(ent.Asc(item.FieldSku)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: list: %w", err)
	}
	dtos, err := buildDTOs(ctx, itms)
	if err != nil {
		return nil, 0, err
	}
	return dtos, total, nil
}

// ListEventItems returns SERVICE-type items that have an event_start_at set,
// ordered by event_start_at ascending (upcoming first).
func (s *Service) ListEventItems(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]ItemDTO, int, error) {
	q := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.IsActive(true),
			item.TypeEQ(item.TypeSERVICE),
			item.EventStartAtNotNil(),
		)

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: count events: %w", err)
	}
	if total == 0 {
		return []ItemDTO{}, 0, nil
	}
	itms, err := q.Order(ent.Asc(item.FieldEventStartAt)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: list events: %w", err)
	}
	dtos := make([]ItemDTO, len(itms))
	for i, it := range itms {
		dtos[i] = *s.mapToDTO(it)
	}
	return dtos, total, nil
}

// DeleteCategory soft-deletes a category (sets is_active=false).
func (s *Service) DeleteCategory(ctx context.Context, tenantID, id uuid.UUID) error {
	existing, err := s.client.ItemCategory.Query().
		Where(itemcategory.TenantID(tenantID), itemcategory.ID(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("items: category not found")
		}
		return fmt.Errorf("items: query category: %w", err)
	}
	if _, err := s.client.ItemCategory.UpdateOneID(existing.ID).SetIsActive(false).Save(ctx); err != nil {
		return fmt.Errorf("items: delete category: %w", err)
	}
	// Invalidate categories cache
	if s.cache != nil {
		s.cache.Invalidate(ctx, sharedcache.Key("inv", "categories", tenantID.String()))
	}
	return nil
}

// CreateCategory creates a new item category for the tenant.
func (s *Service) CreateCategory(ctx context.Context, tenantID uuid.UUID, dto CategoryDTO) (*CategoryDTO, error) {
	q := s.client.ItemCategory.Create().
		SetTenantID(tenantID).
		SetName(dto.Name).
		SetIsActive(true)
	if dto.Code != "" {
		q = q.SetCode(dto.Code)
	}
	if dto.Description != "" {
		q = q.SetDescription(dto.Description)
	}
	if dto.Icon != "" {
		q = q.SetIcon(dto.Icon)
	}
	if dto.ParentID != nil {
		q = q.SetParentID(*dto.ParentID)
	}
	c, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create category: %w", err)
	}
	if s.cache != nil {
		s.cache.Invalidate(ctx, sharedcache.Key("inv", "categories", tenantID.String()))
	}
	return &CategoryDTO{
		ID:          c.ID,
		Name:        c.Name,
		Code:        c.Code,
		Description: c.Description,
		Icon:        c.Icon,
		ParentID:    c.ParentID,
		IsActive:    c.IsActive,
	}, nil
}

// UpdateCategory updates an existing item category.
func (s *Service) UpdateCategory(ctx context.Context, tenantID, id uuid.UUID, dto CategoryDTO) (*CategoryDTO, error) {
	_, err := s.client.ItemCategory.Query().
		Where(itemcategory.TenantID(tenantID), itemcategory.ID(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: category not found")
		}
		return nil, fmt.Errorf("items: query category for update: %w", err)
	}
	q := s.client.ItemCategory.UpdateOneID(id).
		SetName(dto.Name).
		SetIsActive(dto.IsActive)
	if dto.Code != "" {
		q = q.SetCode(dto.Code)
	}
	if dto.Description != "" {
		q = q.SetDescription(dto.Description)
	}
	if dto.Icon != "" {
		q = q.SetIcon(dto.Icon)
	}
	if dto.ParentID != nil {
		q = q.SetParentID(*dto.ParentID)
	} else {
		q = q.ClearParentID()
	}
	c, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: update category: %w", err)
	}
	if s.cache != nil {
		s.cache.Invalidate(ctx, sharedcache.Key("inv", "categories", tenantID.String()))
	}
	return &CategoryDTO{
		ID:          c.ID,
		Name:        c.Name,
		Code:        c.Code,
		Description: c.Description,
		Icon:        c.Icon,
		ParentID:    c.ParentID,
		IsActive:    c.IsActive,
	}, nil
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
		// Build a name lookup map for parent_name resolution
		nameMap := make(map[uuid.UUID]string, len(cats))
		for _, c := range cats {
			nameMap[c.ID] = c.Name
		}
		dtos := make([]CategoryDTO, len(cats))
		for i, c := range cats {
			dto := CategoryDTO{
				ID:          c.ID,
				Name:        c.Name,
				Code:        c.Code,
				Description: c.Description,
				Icon:        s.resolveMediaURL(c.Icon),
				ParentID:    c.ParentID,
				IsActive:    c.IsActive,
			}
			if c.ParentID != nil {
				if pName, ok := nameMap[*c.ParentID]; ok {
					dto.ParentName = pName
				}
			}
			dtos[i] = dto
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

	tags := dto.Tags
	if tags == nil {
		tags = []string{}
	}

	createBuilder := tx.Item.Create().
		SetTenantID(tenantID).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetNillableDescription(&dto.Description).
		SetNillableCategoryID(dto.CategoryID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetTags(tags).
		SetMetadata(dto.Metadata).
		SetNillableCostPrice(dto.CostPrice).
		SetRequiresAgeVerification(dto.RequiresAgeVerification).
		SetIsPerishable(dto.IsPerishable).
		SetTrackLots(dto.TrackLots).
		SetTrackSerialNumbers(dto.TrackSerialNumbers).
		SetTaxInclusive(dto.TaxInclusive)
	if dto.Barcode != "" {
		createBuilder = createBuilder.SetBarcode(dto.Barcode)
	}
	if dto.BarcodeType != "" {
		createBuilder = createBuilder.SetBarcodeType(dto.BarcodeType)
	}
	if dto.TaxCodeID != "" {
		createBuilder = createBuilder.SetTaxCodeID(dto.TaxCodeID)
	}
	i, err := createBuilder.Save(ctx)
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

		// If "add to all outlets" is requested, create balances for all other active warehouses
		if dto.AddToAllOutlets {
			allWarehouses, whsErr := tx.Warehouse.Query().
				Where(
					warehouse.TenantID(tenantID),
					warehouse.IsActive(true),
					warehouse.IDNEQ(wh.ID), // skip the default warehouse (already created above)
				).
				All(ctx)
			if whsErr == nil {
				for _, w := range allWarehouses {
					_, balErr := tx.InventoryBalance.Create().
						SetTenantID(tenantID).
						SetItemID(i.ID).
						SetWarehouseID(w.ID).
						SetOnHand(initialQty).
						SetAvailable(initialQty).
						SetReserved(0).
						SetReorderLevel(reorderLevel).
						SetUnitOfMeasure(uom).
						Save(ctx)
					if balErr != nil {
						s.log.Warn("items: create balance for additional warehouse failed",
							zap.Error(balErr), zap.String("sku", dto.SKU), zap.String("warehouse", w.Code))
					}
				}
			}
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
		AggregateType: "inventory",
		AggregateID:   i.ID,
		EventType:     "item.created",
		Payload: map[string]any{
			"id":                        i.ID,
			"sku":                       i.Sku,
			"name":                      i.Name,
			"description":               i.Description,
			"type":                      i.Type,
			"category_id":               i.CategoryID,
			"category_name":             categoryName,
			"unit_id":                   i.UnitID,
			"unit_name":                 unitName,
			"is_active":                 i.IsActive,
			"image_url":                 i.ImageURL,
			"tags":                      i.Tags,
			"barcode":                   i.Barcode,
			"barcode_type":              i.BarcodeType,
			"requires_age_verification": i.RequiresAgeVerification,
			"is_controlled_substance":   i.IsControlledSubstance,
			"is_perishable":             i.IsPerishable,
			"track_serial_numbers":      i.TrackSerialNumbers,
			"track_lots":                i.TrackLots,
			"weight_kg":                 i.WeightKg,
			"dimensions_cm":             i.DimensionsCm,
			"duration_minutes":          i.DurationMinutes,
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
		SetPayload(json.RawMessage(payload)).
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

	updateTags := dto.Tags
	if updateTags == nil {
		updateTags = []string{}
	}

	updateBuilder := tx.Item.UpdateOneID(id).
		Where(item.TenantID(tenantID)).
		SetName(dto.Name).
		SetNillableDescription(&dto.Description).
		SetNillableCategoryID(dto.CategoryID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetTags(updateTags).
		SetMetadata(dto.Metadata).
		SetNillableCostPrice(dto.CostPrice).
		SetRequiresAgeVerification(dto.RequiresAgeVerification).
		SetIsPerishable(dto.IsPerishable).
		SetTrackLots(dto.TrackLots).
		SetTrackSerialNumbers(dto.TrackSerialNumbers).
		SetTaxInclusive(dto.TaxInclusive)
	if dto.Barcode != "" {
		updateBuilder = updateBuilder.SetBarcode(dto.Barcode)
	}
	if dto.BarcodeType != "" {
		updateBuilder = updateBuilder.SetBarcodeType(dto.BarcodeType)
	}
	if dto.TaxCodeID != "" {
		updateBuilder = updateBuilder.SetTaxCodeID(dto.TaxCodeID)
	}

	i, err := updateBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: update item: %w", err)
	}

	// Update reorder level/quantity on all InventoryBalance records for this item if provided.
	if dto.ReorderLevel > 0 || dto.ReorderQuantity > 0 {
		bals, balErr := tx.InventoryBalance.Query().
			Where(inventorybalance.ItemID(i.ID), inventorybalance.TenantID(tenantID)).
			All(ctx)
		if balErr == nil {
			for _, bal := range bals {
				upd := tx.InventoryBalance.UpdateOneID(bal.ID)
				if dto.ReorderLevel > 0 {
					upd = upd.SetReorderLevel(dto.ReorderLevel)
				}
				if dto.ReorderQuantity > 0 {
					upd = upd.SetReorderQuantity(dto.ReorderQuantity)
				}
				_, _ = upd.Save(ctx)
			}
		}
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
		AggregateType: "inventory",
		AggregateID:   i.ID,
		EventType:     "item.updated",
		Payload: map[string]any{
			"id":                        i.ID,
			"sku":                       i.Sku,
			"name":                      i.Name,
			"description":               i.Description,
			"type":                      i.Type,
			"category_id":               i.CategoryID,
			"category_name":             categoryName,
			"unit_id":                   i.UnitID,
			"unit_name":                 unitName,
			"is_active":                 i.IsActive,
			"image_url":                 i.ImageURL,
			"tags":                      i.Tags,
			"barcode":                   i.Barcode,
			"barcode_type":              i.BarcodeType,
			"requires_age_verification": i.RequiresAgeVerification,
			"is_controlled_substance":   i.IsControlledSubstance,
			"is_perishable":             i.IsPerishable,
			"track_serial_numbers":      i.TrackSerialNumbers,
			"track_lots":                i.TrackLots,
			"weight_kg":                 i.WeightKg,
			"dimensions_cm":             i.DimensionsCm,
			"duration_minutes":          i.DurationMinutes,
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
		SetPayload(json.RawMessage(payload)).
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
