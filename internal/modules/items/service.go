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

	entdialect "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itembrand"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/itemvariant"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/reservation"
	"github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// includeVariantsKey is a context flag set by the HTTP layer (?include=variants)
// to opt into eager-loading the variants edge on item lists. Variant loading is
// gated because it is wasteful for large catalogs that don't need variations.
type includeVariantsKey struct{}

// WithIncludeVariants returns a context that instructs ListItems to eager-load
// each item's active variants and surface them on the DTO.
func WithIncludeVariants(ctx context.Context) context.Context {
	return context.WithValue(ctx, includeVariantsKey{}, true)
}

func includeVariants(ctx context.Context) bool {
	v, _ := ctx.Value(includeVariantsKey{}).(bool)
	return v
}

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
	BrandID         *uuid.UUID     `json:"brand_id,omitempty"`
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
	BrandName       string         `json:"brand_name,omitempty"`
	BrandCode       string         `json:"brand_code,omitempty"`
	// Item-attribute fields (retail / pharmacy)
	Manufacturer string `json:"manufacturer,omitempty"` // retail/pharmacy
	Model        string `json:"model,omitempty"`        // retail only
	// Product variations — surfaced from the ItemVariant edge so retail can sell variations.
	// HasVariants is always populated; Variants is populated when variants are eager-loaded
	// (inline for single-item reads, or for the list when ?include=variants is requested).
	HasVariants bool         `json:"has_variants"`
	Variants    []VariantDTO `json:"variants,omitempty"`
	// Extended fields for POS, logistics, compliance
	Barcode                 string             `json:"barcode,omitempty"`
	BarcodeType             string             `json:"barcode_type,omitempty"`
	RequiresAgeVerification bool               `json:"requires_age_verification"`
	IsControlledSubstance   bool               `json:"is_controlled_substance"` // pharmacy: scheduled drugs
	IsPerishable            bool               `json:"is_perishable"`
	TrackLots               bool               `json:"track_lots"`
	TrackSerialNumbers      bool               `json:"track_serial_numbers"`
	ShelfLifeDays           *int               `json:"shelf_life_days,omitempty"` // default shelf life; seeds lot expiry at receipt
	WeightKg                *float64           `json:"weight_kg,omitempty"`
	DimensionsCm            map[string]float64 `json:"dimensions_cm,omitempty"`
	DurationMinutes         *int               `json:"duration_minutes,omitempty"` // service duration (salon/barber)
	// Cost / pricing fields
	CostPrice *float64 `json:"cost_price,omitempty"`
	// Effective customer-facing price + tax split — enriched at read time for the POS/ordering
	// proxies (recipe selling price → default pricing tier → cost+margin suggestion).
	SellingPrice *float64 `json:"selling_price,omitempty"` // what the customer pays (gross when tax-inclusive)
	NetPrice     *float64 `json:"net_price,omitempty"`     // selling price excluding tax
	TaxAmount    *float64 `json:"tax_amount,omitempty"`    // tax portion of the selling price
	TaxRate      *float64 `json:"tax_rate,omitempty"`      // VAT rate % applied (resolved from treasury-api)
	// Purchase / supplier fields — enable auto EP-cost calculation
	PurchasePrice    *float64 `json:"purchase_price,omitempty"`
	PurchasePackSize *float64 `json:"purchase_pack_size,omitempty"`
	PurchaseUnit     string   `json:"purchase_unit,omitempty"`
	YieldPct         *float64 `json:"yield_pct,omitempty"` // 0 < y <= 1; default 1.0
	// KRA eTIMS tax fields
	TaxCodeID    string `json:"tax_code_id,omitempty"`
	TaxInclusive bool   `json:"tax_inclusive"`
	// Event capacity fields — SERVICE type only
	TotalCapacity  *int       `json:"total_capacity,omitempty"`
	BookedCapacity *int       `json:"booked_capacity,omitempty"`
	EventStartAt   *time.Time `json:"event_start_at,omitempty"`
	EventEndAt     *time.Time `json:"event_end_at,omitempty"`
	EventVenue     *string    `json:"event_venue,omitempty"`
	// Hospitality fields — room-type / facility / amenity SERVICE items
	UseCase          string   `json:"use_case,omitempty"`  // RETAIL | FOOD_BEVERAGE | HOSPITALITY_ROOM | HOSPITALITY_FACILITY | CONFERENCE | SALON_SERVICE | AMENITY
	MealPlan         *string  `json:"meal_plan,omitempty"` // RO | BB | HB | FB | AI
	OccupancyBasis   *string  `json:"occupancy_basis,omitempty"`
	MaxAdults        *int     `json:"max_adults,omitempty"`
	MaxChildren      *int     `json:"max_children,omitempty"`
	ExtraBedAllowed  bool     `json:"extra_bed_allowed"`
	SingleSupplement *float64 `json:"single_supplement,omitempty"`
	// Current stock levels (aggregated across all warehouses).
	// Populated by ListItems; nil when no balance row exists.
	Available *float64  `json:"available,omitempty"`
	OnHand    *float64  `json:"on_hand,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// VariantDTO is the catalog-facing projection of an ItemVariant. Surfaced on the item
// so POS/ordering catalog proxies can offer variations for sale.
type VariantDTO struct {
	ID         uuid.UUID         `json:"id"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	Price      float64           `json:"price"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Barcode    string            `json:"barcode,omitempty"`
	IsActive   bool              `json:"is_active"`
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
	IsGlobal    bool       `json:"is_global,omitempty"`
}

// StockAvailability matches the DTO expected by the ordering-backend client.
type StockAvailability struct {
	ItemID              uuid.UUID  `json:"item_id"`
	SKU                 string     `json:"sku"`
	WarehouseID         uuid.UUID  `json:"warehouse_id"`
	OnHand              float64    `json:"on_hand"`
	Available           float64    `json:"available"`
	Reserved            float64    `json:"reserved"`
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
	taxResolver  TaxResolver // resolves VAT rate from treasury-api (optional; nil → DefaultVATRate)
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

// SetTaxResolver injects the treasury-api tax-rate resolver (optional).
func (s *Service) SetTaxResolver(r TaxResolver) {
	s.taxResolver = r
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

	balMap := make(map[uuid.UUID]float64, len(balances))
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
		OnHand:        float64(availablePortions),
		Available:     float64(availablePortions),
		Reserved:      0,
		UnitOfMeasure: rec.UnitOfMeasure,
		UpdatedAt:     itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

// BOMAvailabilityResult represents the BOM-aware availability for a single SKU.
type BOMAvailabilityResult struct {
	SKU           string    `json:"sku"`
	ItemID        uuid.UUID `json:"item_id"`
	Available     float64   `json:"available"`
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
	dto := &ItemDTO{
		ID:                      i.ID,
		SKU:                     i.Sku,
		Name:                    i.Name,
		Description:             i.Description,
		CategoryID:              i.CategoryID,
		BrandID:                 i.BrandID,
		UnitID:                  i.UnitID,
		Type:                    string(i.Type),
		IsActive:                i.IsActive,
		Manufacturer:            i.Manufacturer,
		Model:                   i.Model,
		ImageURL:                s.resolveMediaURL(i.ImageURL),
		Tags:                    i.Tags,
		Metadata:                i.Metadata,
		Barcode:                 i.Barcode,
		BarcodeType:             i.BarcodeType,
		RequiresAgeVerification: i.RequiresAgeVerification,
		IsControlledSubstance:   i.IsControlledSubstance,
		IsPerishable:            i.IsPerishable,
		TrackLots:               i.TrackLots,
		TrackSerialNumbers:      i.TrackSerialNumbers,
		ShelfLifeDays:           i.ShelfLifeDays,
		WeightKg:                i.WeightKg,
		DimensionsCm:            i.DimensionsCm,
		DurationMinutes:         i.DurationMinutes,
		CostPrice:               i.CostPrice,
		PurchasePrice:           i.PurchasePrice,
		PurchasePackSize:        i.PurchasePackSize,
		PurchaseUnit:            i.PurchaseUnit,
		YieldPct:                i.YieldPct,
		TaxCodeID:               i.TaxCodeID,
		TaxInclusive:            i.TaxInclusive,
		TotalCapacity:           i.TotalCapacity,
		BookedCapacity:          i.BookedCapacity,
		EventStartAt:            i.EventStartAt,
		EventEndAt:              i.EventEndAt,
		EventVenue:              i.EventVenue,
		UseCase:                 string(i.UseCase),
		MealPlan:                enumPtrToStr(i.MealPlan),
		OccupancyBasis:          enumPtrToStr(i.OccupancyBasis),
		MaxAdults:               i.MaxAdults,
		MaxChildren:             i.MaxChildren,
		ExtraBedAllowed:         i.ExtraBedAllowed,
		SingleSupplement:        i.SingleSupplement,
		CreatedAt:               i.CreatedAt,
		UpdatedAt:               i.UpdatedAt,
	}
	// Surface product variations when the variants edge has been eager-loaded.
	// Only active variants are exposed for sale; HasVariants reflects that count.
	if variants, err := i.Edges.VariantsOrErr(); err == nil {
		for _, v := range variants {
			if !v.IsActive {
				continue
			}
			dto.Variants = append(dto.Variants, VariantDTO{
				ID:         v.ID,
				SKU:        v.Sku,
				Name:       v.Name,
				Price:      v.Price,
				Attributes: v.Attributes,
				Barcode:    v.Barcode,
				IsActive:   v.IsActive,
			})
		}
		dto.HasVariants = len(dto.Variants) > 0
	}
	return dto
}

// enumPtrToStr converts a nillable Ent enum pointer to a *string for DTO output.
func enumPtrToStr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

// ListItems returns a paginated list of items for a tenant with DB-level filtering.
// statusFilter: "" or "active" = active only (default), "inactive" = inactive only, "all" = both.
// outletID: when set, restricts items to those with a balance in the outlet's warehouses or shared warehouses (outlet_id IS NULL).
func (s *Service) ListItems(ctx context.Context, tenantID uuid.UUID, typeFilter, statusFilter string, limit, offset int, categoryID *uuid.UUID, unitID *uuid.UUID, search string, outletID *uuid.UUID, useCase string, tagsFilter ...string) ([]ItemDTO, int, error) {
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
		if len(wIDs) > 0 {
			bals, _ := s.client.InventoryBalance.Query().
				Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.WarehouseIDIn(wIDs...)).
				All(ctx)
			idSet := make(map[uuid.UUID]struct{}, len(bals))
			for _, b := range bals {
				idSet[b.ItemID] = struct{}{}
			}
			// Only scope to stocked items when the outlet actually HAS stock. A freshly
			// provisioned outlet whose warehouse has no balances yet (e.g. a retail till that
			// hasn't received stock) would otherwise get an EMPTY catalog and be unable to sell
			// anything — which is what made a retail-use-case POS show no items at all. In that
			// case leave outletItemIDs nil and fall back to the type/category-filtered catalog,
			// so the outlet's sellable items still appear (their stock surfaces as 0 until goods
			// are received).
			if len(idSet) > 0 {
				outletItemIDs = make([]uuid.UUID, 0, len(idSet))
				for id := range idSet {
					outletItemIDs = append(outletItemIDs, id)
				}
			}
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
		if useCase != "" {
			q = q.Where(item.UseCaseEQ(item.UseCase(useCase)))
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
		brandIDs := make([]uuid.UUID, 0, len(itms))
		itemIDs := make([]uuid.UUID, 0, len(itms))
		for _, it := range itms {
			if it.CategoryID != nil {
				catIDs = append(catIDs, *it.CategoryID)
			}
			if it.BrandID != nil {
				brandIDs = append(brandIDs, *it.BrandID)
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
		type brandMeta struct{ name, code string }
		brandInfo := make(map[uuid.UUID]brandMeta)
		if len(brandIDs) > 0 {
			brands, _ := s.client.ItemBrand.Query().Where(itembrand.IDIn(brandIDs...)).All(innerCtx)
			for _, b := range brands {
				brandInfo[b.ID] = brandMeta{name: b.Name, code: b.Code}
			}
		}
		// Load balances per item to surface reorder_level, reorder_quantity, and total available/on_hand.
		type balSummary struct {
			reorderLevel    int
			reorderQuantity int
			available       float64
			onHand          float64
		}
		balMap := make(map[uuid.UUID]balSummary, len(itemIDs))
		if len(itemIDs) > 0 {
			bals, _ := s.client.InventoryBalance.Query().
				Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.ItemIDIn(itemIDs...)).
				All(innerCtx)
			for _, b := range bals {
				prev := balMap[b.ItemID]
				if prev.reorderLevel == 0 {
					prev.reorderLevel = b.ReorderLevel
					prev.reorderQuantity = b.ReorderQuantity
				}
				prev.available += b.Available
				prev.onHand += b.OnHand
				balMap[b.ItemID] = prev
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
			if it.BrandID != nil {
				if bm, ok := brandInfo[*it.BrandID]; ok {
					dto.BrandName = bm.name
					dto.BrandCode = bm.code
				}
			}
			if bs, ok := balMap[it.ID]; ok {
				dto.ReorderLevel = bs.reorderLevel
				dto.ReorderQuantity = bs.reorderQuantity
				dto.Available = &bs.available
				dto.OnHand = &bs.onHand
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
		// Enrich with effective selling price + tax split for POS/ordering proxies.
		s.enrichPrices(innerCtx, tenantID, cfg, dtos)
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
	listQuery := buildQuery().Order(ent.Asc(item.FieldSku)).Limit(limit).Offset(offset)
	if includeVariants(ctx) {
		// Eager-load active variants so mapToDTO can surface them inline.
		listQuery = listQuery.WithVariants(func(vq *ent.ItemVariantQuery) {
			vq.Where(itemvariant.IsActive(true)).Order(ent.Asc(itemvariant.FieldName))
		})
	}
	itms, err := listQuery.All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: list: %w", err)
	}
	dtos, err := buildDTOs(ctx, itms)
	if err != nil {
		return nil, 0, err
	}
	return dtos, total, nil
}

// ListItemVariants returns the active product variations for a single item.
// Surfaces the existing ItemVariant edge so retail/POS can sell variations.
func (s *Service) ListItemVariants(ctx context.Context, tenantID, itemID uuid.UUID) ([]VariantDTO, error) {
	// Ensure the item belongs to the tenant before exposing its variants.
	exists, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.ID(itemID)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: lookup item for variants: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("items: item not found")
	}
	variants, err := s.client.ItemVariant.Query().
		Where(itemvariant.ItemID(itemID), itemvariant.IsActive(true)).
		Order(ent.Asc(itemvariant.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: list variants: %w", err)
	}
	dtos := make([]VariantDTO, 0, len(variants))
	for _, v := range variants {
		dtos = append(dtos, VariantDTO{
			ID:         v.ID,
			SKU:        v.Sku,
			Name:       v.Name,
			Price:      v.Price,
			Attributes: v.Attributes,
			Barcode:    v.Barcode,
			IsActive:   v.IsActive,
		})
	}
	return dtos, nil
}

// ListEventItems returns all active SERVICE-type items (one-time and recurring),
// ordered by event_start_at ascending; recurring events without a date appear last.
func (s *Service) ListEventItems(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]ItemDTO, int, error) {
	q := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.IsActive(true),
			item.TypeEQ(item.TypeSERVICE),
		)

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: count events: %w", err)
	}
	if total == 0 {
		return []ItemDTO{}, 0, nil
	}
	itms, err := q.
		Order(func(s *entdialect.Selector) {
			s.OrderExpr(entdialect.Expr(item.FieldEventStartAt + " ASC NULLS LAST"))
		}).
		Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("items: list events: %w", err)
	}
	dtos := make([]ItemDTO, len(itms))
	for i, it := range itms {
		dtos[i] = *s.mapToDTO(it)
	}
	cfg, _ := s.client.TenantInventoryConfig.Query().
		Where(tenantinventoryconfig.TenantID(tenantID)).
		Only(ctx)
	s.enrichPrices(ctx, tenantID, cfg, dtos)
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
// When dto.IsGlobal is true, the category is visible to all tenants.
func (s *Service) CreateCategory(ctx context.Context, tenantID uuid.UUID, dto CategoryDTO) (*CategoryDTO, error) {
	q := s.client.ItemCategory.Create().
		SetTenantID(tenantID).
		SetName(dto.Name).
		SetIsActive(true).
		SetIsGlobal(dto.IsGlobal)
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
		// Invalidate both the tenant-specific key and force a global refresh.
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
		IsGlobal:    c.IsGlobal,
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
			Where(
				itemcategory.IsActive(true),
				itemcategory.Or(
					itemcategory.TenantID(tenantID),
					itemcategory.IsGlobal(true),
				),
			).
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

// BrandDTO is a lightweight item-brand projection used by bulk import for
// name-based brand resolution / auto-create. Mirrors CategoryDTO.
type BrandDTO struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Code     string    `json:"code,omitempty"`
	IsActive bool      `json:"is_active"`
}

// ListBrands returns all active item brands for a tenant.
// Mirrors ListCategories so bulk import can resolve brands by name.
func (s *Service) ListBrands(ctx context.Context, tenantID uuid.UUID) ([]BrandDTO, error) {
	brands, err := s.client.ItemBrand.Query().
		Where(itembrand.TenantID(tenantID), itembrand.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: list brands: %w", err)
	}
	dtos := make([]BrandDTO, len(brands))
	for i, b := range brands {
		dtos[i] = BrandDTO{
			ID:       b.ID,
			Name:     b.Name,
			Code:     b.Code,
			IsActive: b.IsActive,
		}
	}
	return dtos, nil
}

// CreateBrand creates a new tenant-scoped item brand.
// Mirrors CreateCategory; the code is required by the ItemBrand schema, so the
// caller slugifies the name when no explicit code is supplied.
func (s *Service) CreateBrand(ctx context.Context, tenantID uuid.UUID, dto BrandDTO) (*BrandDTO, error) {
	b, err := s.client.ItemBrand.Create().
		SetTenantID(tenantID).
		SetName(dto.Name).
		SetCode(dto.Code).
		SetIsActive(true).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create brand: %w", err)
	}
	return &BrandDTO{
		ID:       b.ID,
		Name:     b.Name,
		Code:     b.Code,
		IsActive: b.IsActive,
	}, nil
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

// resolveEPCost auto-computes cost_price (EP unit cost) from purchase fields when all three
// are present and cost_price was not explicitly provided.
// Formula: cost_price = purchase_price / purchase_pack_size / yield_pct
func resolveEPCost(dto *ItemDTO) {
	if dto.CostPrice != nil {
		return // caller provided cost_price explicitly — respect it
	}
	if dto.PurchasePrice == nil || dto.PurchasePackSize == nil {
		return
	}
	if *dto.PurchasePackSize <= 0 {
		return
	}
	yieldPct := 1.0
	if dto.YieldPct != nil && *dto.YieldPct > 0 && *dto.YieldPct <= 1 {
		yieldPct = *dto.YieldPct
	}
	ep := *dto.PurchasePrice / *dto.PurchasePackSize / yieldPct
	dto.CostPrice = &ep
}

// resolveReorderLevel returns the effective reorder_level for a new item.
// Resolution order: explicit DTO value → unit-based tenant default → global tenant default → hard fallback.
func resolveReorderLevel(ctx context.Context, client *ent.Client, tenantID uuid.UUID, unitAbbr string, dtoLevel int) int {
	if dtoLevel > 0 {
		return dtoLevel
	}
	cfg, err := client.TenantInventoryConfig.Query().
		Where(tenantinventoryconfig.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return 1 // hard fallback
	}
	if unitAbbr != "" && cfg.UnitReorderDefaults != nil {
		if v, ok := cfg.UnitReorderDefaults[unitAbbr]; ok && v > 0 {
			return v
		}
	}
	if cfg.DefaultReorderLevel > 0 {
		return cfg.DefaultReorderLevel
	}
	return 1
}

// isStockTracked reports whether an item type holds physical stock that should
// appear in inventory balances. SERVICE and VOUCHER are non-stockable (e.g.
// "Conference charges"); RECIPE availability is derived from its BOM, so it is
// not tracked as a balance directly either. Only GOODS, INGREDIENT and EQUIPMENT
// carry real on-hand stock.
func isStockTracked(t item.Type) bool {
	switch t {
	case item.TypeGOODS, item.TypeINGREDIENT, item.TypeEQUIPMENT:
		return true
	default:
		return false
	}
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
	// Auto-compute EP cost from purchase fields when not explicitly set.
	resolveEPCost(&dto)

	// Default the tax code from the tenant compliance config when unset — treasury/eTIMS reads
	// this off the item on POS/ordering sales. The inclusive-vs-exclusive behaviour itself is
	// resolved at read time from the tenant setting, so it always follows the current setting.
	if dto.TaxCodeID == "" {
		if cfg, cErr := s.client.TenantInventoryConfig.Query().
			Where(tenantinventoryconfig.TenantID(tenantID)).Only(ctx); cErr == nil && cfg.DefaultTaxCode != "" {
			dto.TaxCodeID = cfg.DefaultTaxCode
		}
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
		SetNillableBrandID(dto.BrandID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetTags(tags).
		SetMetadata(dto.Metadata).
		SetNillableCostPrice(dto.CostPrice).
		SetNillablePurchasePrice(dto.PurchasePrice).
		SetNillablePurchasePackSize(dto.PurchasePackSize).
		SetNillableYieldPct(dto.YieldPct).
		SetRequiresAgeVerification(dto.RequiresAgeVerification).
		SetIsControlledSubstance(dto.IsControlledSubstance).
		SetIsPerishable(dto.IsPerishable).
		SetTrackLots(dto.TrackLots).
		SetTrackSerialNumbers(dto.TrackSerialNumbers).
		SetNillableShelfLifeDays(dto.ShelfLifeDays).
		SetNillableDurationMinutes(dto.DurationMinutes).
		SetTaxInclusive(dto.TaxInclusive)
	if dto.PurchaseUnit != "" {
		createBuilder = createBuilder.SetPurchaseUnit(dto.PurchaseUnit)
	}
	if dto.Manufacturer != "" {
		createBuilder = createBuilder.SetManufacturer(dto.Manufacturer)
	}
	if dto.Model != "" {
		createBuilder = createBuilder.SetModel(dto.Model)
	}
	if dto.Barcode != "" {
		createBuilder = createBuilder.SetBarcode(dto.Barcode)
	}
	if dto.BarcodeType != "" {
		createBuilder = createBuilder.SetBarcodeType(dto.BarcodeType)
	}
	if dto.TaxCodeID != "" {
		createBuilder = createBuilder.SetTaxCodeID(dto.TaxCodeID)
	}
	if dto.TotalCapacity != nil {
		createBuilder = createBuilder.SetTotalCapacity(*dto.TotalCapacity)
	}
	if dto.EventStartAt != nil {
		createBuilder = createBuilder.SetEventStartAt(*dto.EventStartAt)
	}
	if dto.EventEndAt != nil {
		createBuilder = createBuilder.SetEventEndAt(*dto.EventEndAt)
	}
	if dto.EventVenue != nil {
		createBuilder = createBuilder.SetEventVenue(*dto.EventVenue)
	}
	if dto.UseCase != "" {
		createBuilder = createBuilder.SetUseCase(item.UseCase(dto.UseCase))
	}
	if dto.MealPlan != nil {
		createBuilder = createBuilder.SetMealPlan(item.MealPlan(*dto.MealPlan))
	}
	if dto.OccupancyBasis != nil {
		createBuilder = createBuilder.SetOccupancyBasis(item.OccupancyBasis(*dto.OccupancyBasis))
	}
	if dto.MaxAdults != nil {
		createBuilder = createBuilder.SetMaxAdults(*dto.MaxAdults)
	}
	if dto.MaxChildren != nil {
		createBuilder = createBuilder.SetMaxChildren(*dto.MaxChildren)
	}
	createBuilder = createBuilder.SetExtraBedAllowed(dto.ExtraBedAllowed)
	if dto.SingleSupplement != nil {
		createBuilder = createBuilder.SetSingleSupplement(*dto.SingleSupplement)
	}
	i, err := createBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: create item: %w", err)
	}

	// Create initial balance if initial_quantity > 0
	initialQty := float64(dto.InitialQuantity)
	if initialQty <= 0 {
		initialQty = 1
	}

	// Auto-seed reorder level from tenant unit defaults when not explicitly provided.
	unitAbbr := ""
	if dto.UnitID != nil {
		if u, uLookupErr := s.client.Unit.Get(ctx, *dto.UnitID); uLookupErr == nil {
			unitAbbr = u.Abbreviation
		}
	}
	reorderLevel := resolveReorderLevel(ctx, s.client, tenantID, unitAbbr, dto.ReorderLevel)

	// Resolve default warehouse
	wh, whErr := s.client.Warehouse.Query().
		Where(
			warehouse.TenantID(tenantID),
			warehouse.IsDefault(true),
			warehouse.IsActive(true),
		).
		First(ctx)
	if whErr == nil && isStockTracked(i.Type) {
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
			"manufacturer":              i.Manufacturer,
			"model":                     i.Model,
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
			"use_case":                  i.UseCase,
			"meal_plan":                 i.MealPlan,
			"occupancy_basis":           i.OccupancyBasis,
			"max_adults":                i.MaxAdults,
			"max_children":              i.MaxChildren,
			"tax_code_id":               i.TaxCodeID,
			"tax_inclusive":             i.TaxInclusive,
			"cost_price":                i.CostPrice,
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

	// Auto-compute EP cost from purchase fields if not explicitly provided.
	resolveEPCost(&dto)

	updateBuilder := tx.Item.UpdateOneID(id).
		Where(item.TenantID(tenantID)).
		SetName(dto.Name).
		SetNillableDescription(&dto.Description).
		SetNillableCategoryID(dto.CategoryID).
		SetNillableBrandID(dto.BrandID).
		SetNillableUnitID(dto.UnitID).
		SetType(item.Type(dto.Type)).
		SetIsActive(dto.IsActive).
		SetNillableImageURL(&dto.ImageURL).
		SetTags(updateTags).
		SetMetadata(dto.Metadata).
		SetNillableCostPrice(dto.CostPrice).
		SetNillablePurchasePrice(dto.PurchasePrice).
		SetNillablePurchasePackSize(dto.PurchasePackSize).
		SetNillableYieldPct(dto.YieldPct).
		SetRequiresAgeVerification(dto.RequiresAgeVerification).
		SetIsControlledSubstance(dto.IsControlledSubstance).
		SetIsPerishable(dto.IsPerishable).
		SetTrackLots(dto.TrackLots).
		SetTrackSerialNumbers(dto.TrackSerialNumbers).
		SetNillableShelfLifeDays(dto.ShelfLifeDays).
		SetNillableDurationMinutes(dto.DurationMinutes).
		SetManufacturer(dto.Manufacturer).
		SetModel(dto.Model).
		SetTaxInclusive(dto.TaxInclusive)
	if dto.PurchaseUnit != "" {
		updateBuilder = updateBuilder.SetPurchaseUnit(dto.PurchaseUnit)
	}
	if dto.Barcode != "" {
		updateBuilder = updateBuilder.SetBarcode(dto.Barcode)
	}
	if dto.BarcodeType != "" {
		updateBuilder = updateBuilder.SetBarcodeType(dto.BarcodeType)
	}
	if dto.TaxCodeID != "" {
		updateBuilder = updateBuilder.SetTaxCodeID(dto.TaxCodeID)
	}
	if dto.TotalCapacity != nil {
		updateBuilder = updateBuilder.SetTotalCapacity(*dto.TotalCapacity)
	}
	// Persist booked_capacity (previously ignored) and prevent overselling an event:
	// booked seats can never exceed total capacity.
	if dto.BookedCapacity != nil {
		if dto.TotalCapacity != nil && *dto.BookedCapacity > *dto.TotalCapacity {
			err = fmt.Errorf("booked_capacity (%d) cannot exceed total_capacity (%d)", *dto.BookedCapacity, *dto.TotalCapacity)
			return nil, err
		}
		if *dto.BookedCapacity < 0 {
			err = fmt.Errorf("booked_capacity cannot be negative")
			return nil, err
		}
		updateBuilder = updateBuilder.SetBookedCapacity(*dto.BookedCapacity)
	}
	if dto.EventStartAt != nil {
		updateBuilder = updateBuilder.SetEventStartAt(*dto.EventStartAt)
	}
	if dto.EventEndAt != nil {
		updateBuilder = updateBuilder.SetEventEndAt(*dto.EventEndAt)
	}
	if dto.EventVenue != nil {
		updateBuilder = updateBuilder.SetEventVenue(*dto.EventVenue)
	}
	if dto.UseCase != "" {
		updateBuilder = updateBuilder.SetUseCase(item.UseCase(dto.UseCase))
	}
	if dto.MealPlan != nil {
		updateBuilder = updateBuilder.SetMealPlan(item.MealPlan(*dto.MealPlan))
	}
	if dto.OccupancyBasis != nil {
		updateBuilder = updateBuilder.SetOccupancyBasis(item.OccupancyBasis(*dto.OccupancyBasis))
	}
	if dto.MaxAdults != nil {
		updateBuilder = updateBuilder.SetMaxAdults(*dto.MaxAdults)
	}
	if dto.MaxChildren != nil {
		updateBuilder = updateBuilder.SetMaxChildren(*dto.MaxChildren)
	}
	updateBuilder = updateBuilder.SetExtraBedAllowed(dto.ExtraBedAllowed)
	if dto.SingleSupplement != nil {
		updateBuilder = updateBuilder.SetSingleSupplement(*dto.SingleSupplement)
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
			"manufacturer":              i.Manufacturer,
			"model":                     i.Model,
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
			"use_case":                  i.UseCase,
			"meal_plan":                 i.MealPlan,
			"occupancy_basis":           i.OccupancyBasis,
			"max_adults":                i.MaxAdults,
			"max_children":              i.MaxChildren,
			"tax_code_id":               i.TaxCodeID,
			"tax_inclusive":             i.TaxInclusive,
			"cost_price":                i.CostPrice,
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
