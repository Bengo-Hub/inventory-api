package items

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Bengo-Hub/httpware/money"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itembrand"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// StockValuationItem is a single line in the stock valuation. Value is computed from the item's
// own InventoryLot cost layers (what each unit actually cost when bought) whenever any exist;
// CostBasis reports which basis was actually used, so a gap in layer coverage is visible on the
// report rather than silently defaulting.
type StockValuationItem struct {
	ItemID       uuid.UUID `json:"item_id"`
	SKU          string    `json:"sku"`
	Name         string    `json:"name"`
	CategoryName string    `json:"category_name,omitempty"`
	BrandName    string    `json:"brand_name,omitempty"`
	OnHand       float64   `json:"on_hand"`
	UnitCost     float64   `json:"unit_cost"`
	Value        float64   `json:"value"`
	// CostBasis is "layers" (valued from InventoryLot cost layers) or "item_cost_fallback"
	// (the item has no active cost layer yet — e.g. pre-cutover stock awaiting the opening-
	// balance backfill — so on_hand × the item's standard cost is used instead).
	CostBasis string `json:"cost_basis"`
}

// StockValuationCategory aggregates value by item category.
type StockValuationCategory struct {
	CategoryID   *uuid.UUID `json:"category_id,omitempty"`
	CategoryName string     `json:"category_name"`
	ItemCount    int        `json:"item_count"`
	TotalUnits   float64    `json:"total_units"`
	TotalValue   float64    `json:"total_value"`
}

// StockValuation is the full inventory valuation report for a tenant.
type StockValuation struct {
	Currency   string                   `json:"currency"`
	TotalValue float64                  `json:"total_value"`
	TotalUnits float64                  `json:"total_units"`
	ItemCount  int                      `json:"item_count"`
	ByCategory []StockValuationCategory `json:"by_category"`
	ByBrand    []StockValuationBrand    `json:"by_brand"`
	// TopItems is one page of items by value (Offset, Limit); totals cover every matching item.
	TopItems []StockValuationItem `json:"top_items"`
	Offset   int                  `json:"offset"`
	Limit    int                  `json:"limit"`
}

// StockValuationBrand aggregates value by item brand.
type StockValuationBrand struct {
	BrandID    *uuid.UUID `json:"brand_id,omitempty"`
	BrandName  string     `json:"brand_name"`
	ItemCount  int        `json:"item_count"`
	TotalUnits float64    `json:"total_units"`
	TotalValue float64    `json:"total_value"`
}

// inventoryValueSQL is the whole-scope stock value in one statement: every active cost layer at
// its own cost, plus, for each (item, warehouse) balance with no active cost-recorded layer, its
// on-hand at the item's standard cost. Negative on-hand (an unsettled oversell) is not an asset,
// so it counts as zero. $1 tenant, $2 warehouses (NULL = every warehouse).
const inventoryValueSQL = `
SELECT
  coalesce((
    SELECT sum(l.quantity * l.cost_price)
    FROM inventory_lots l
    WHERE l.tenant_id = $1 AND l.status = 'active' AND l.quantity > 0 AND l.cost_price IS NOT NULL
      AND ($2::uuid[] IS NULL OR l.warehouse_id = ANY($2::uuid[]))
  ), 0)
  +
  coalesce((
    SELECT sum(greatest(b.on_hand, 0) * coalesce(i.cost_price, 0))
    FROM inventory_balances b
    JOIN items i ON i.id = b.item_id
    WHERE b.tenant_id = $1
      AND ($2::uuid[] IS NULL OR b.warehouse_id = ANY($2::uuid[]))
      AND NOT EXISTS (
        SELECT 1 FROM inventory_lots l
        WHERE l.tenant_id = $1 AND l.item_id = b.item_id AND l.warehouse_id = b.warehouse_id
          AND l.status = 'active' AND l.quantity > 0 AND l.cost_price IS NOT NULL
      )
  ), 0)`

// InventoryValue is the total value of stock held in scope, computed in the database with one
// aggregate (see inventoryValueSQL) so the dashboard never loads every balance and lot to add
// them up. warehouseIDs scopes it the same way StockValuation does: nil is every warehouse, a
// non-nil empty slice (an outlet with no warehouse) is zero.
func (s *Service) InventoryValue(ctx context.Context, tenantID uuid.UUID, warehouseIDs []uuid.UUID) (float64, error) {
	if warehouseIDs != nil && len(warehouseIDs) == 0 {
		return 0, nil
	}
	if s.reportDB == nil {
		return 0, errReportDBMissing
	}
	var total float64
	if err := s.reportDB.QueryRowContext(ctx, inventoryValueSQL, tenantID, warehouseParam(warehouseIDs)).Scan(&total); err != nil {
		return 0, fmt.Errorf("inventory value: %w", err)
	}
	return money.Round2(total), nil
}

// valuationTopLimit is the default page of listed items.
const valuationTopLimit = 20

// StockValuation computes each item's inventory value from its own InventoryLot cost layers
// (Σ layer.quantity × layer.cost_price) — what was actually paid for the stock on hand, never a
// single mutable Item.cost_price retroactively applied to everything. Items with no active cost
// layer yet (pre-cutover stock awaiting the opening-balance backfill) fall back to
// on_hand × the item's standard cost, and are flagged via CostBasis so the gap is visible on the
// report rather than silently blended in. Returns the grand total, a per-category breakdown, and
// the top-20 items by value.
//
// warehouseIDs scopes the whole report to those locations (an outlet's own warehouses, or one
// location; nil = tenant-wide, every warehouse blended together), the same scope Deadstock uses. A
// non-nil empty scope (an outlet with no warehouse) is an empty report. f narrows by category,
// brand and search and pages TopItems; totals and breakdowns cover every matching item.
func (s *Service) StockValuation(ctx context.Context, tenantID uuid.UUID, warehouseIDs []uuid.UUID, f ReportFilter) (*StockValuation, error) {
	offset, limit := f.page(valuationTopLimit)
	if warehouseIDs != nil && len(warehouseIDs) == 0 {
		return &StockValuation{Currency: "KES", Offset: offset, Limit: limit,
			ByCategory: []StockValuationCategory{}, ByBrand: []StockValuationBrand{}, TopItems: []StockValuationItem{}}, nil
	}
	balQuery := s.client.InventoryBalance.Query().Where(inventorybalance.TenantID(tenantID))
	if warehouseIDs != nil {
		balQuery = balQuery.Where(inventorybalance.WarehouseIDIn(warehouseIDs...))
	}
	var itemPreds []predicate.Item
	if len(f.CategoryIDs) > 0 {
		itemPreds = append(itemPreds, entitem.CategoryIDIn(f.CategoryIDs...))
	}
	if len(f.BrandIDs) > 0 {
		itemPreds = append(itemPreds, entitem.BrandIDIn(f.BrandIDs...))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		itemPreds = append(itemPreds, entitem.Or(entitem.NameContainsFold(q), entitem.SkuContainsFold(q)))
	}
	if len(itemPreds) > 0 {
		balQuery = balQuery.Where(inventorybalance.HasItemWith(itemPreds...))
	}
	balances, err := balQuery.WithItem(func(q *ent.ItemQuery) { q.WithUnits() }).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock valuation: query balances: %w", err)
	}

	cats, _ := s.client.ItemCategory.Query().Where(itemcategory.TenantID(tenantID)).All(ctx)
	catName := make(map[uuid.UUID]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = c.Name
	}
	brands, _ := s.client.ItemBrand.Query().Where(itembrand.TenantID(tenantID)).All(ctx)
	brandName := make(map[uuid.UUID]string, len(brands))
	for _, b := range brands {
		brandName[b.ID] = b.Name
	}
	scopedItems := make([]uuid.UUID, 0, len(balances))
	seenItem := make(map[uuid.UUID]bool, len(balances))
	for _, b := range balances {
		if !seenItem[b.ItemID] {
			seenItem[b.ItemID] = true
			scopedItems = append(scopedItems, b.ItemID)
		}
	}

	// Per-item layer totals: quantity + value across every active, cost-recorded lot (real lots
	// AND internal cost layers — same table). Scoped to the same warehouse as the balance query
	// above when one is set, so a location's valuation only draws on cost layers actually held
	// at that location — mixing in another warehouse's layers would misprice this one's stock.
	lotQuery := s.client.InventoryLot.Query().
		Where(entlot.TenantID(tenantID), entlot.StatusEQ(entlot.StatusActive), entlot.CostPriceNotNil())
	if warehouseIDs != nil {
		lotQuery = lotQuery.Where(entlot.WarehouseIDIn(warehouseIDs...))
	}
	if len(itemPreds) > 0 {
		// Only the filtered items' layers; without a filter every scoped layer is needed anyway.
		lotQuery = lotQuery.Where(entlot.ItemIDIn(scopedItems...))
	}
	layers, err := lotQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock valuation: query cost layers: %w", err)
	}
	type layerAgg struct{ qty, value float64 }
	layerTotals := make(map[uuid.UUID]*layerAgg, len(layers))
	for _, l := range layers {
		la := layerTotals[l.ItemID]
		if la == nil {
			la = &layerAgg{}
			layerTotals[l.ItemID] = la
		}
		la.qty += l.Quantity
		la.value += l.Quantity * *l.CostPrice
	}

	type agg struct {
		onHand, cost float64
		sku, name    string
		cat, brand   string
		catID        *uuid.UUID
		brandID      *uuid.UUID
	}
	perItem := make(map[uuid.UUID]*agg)
	for _, b := range balances {
		it := b.Edges.Item
		if it == nil {
			continue
		}
		a := perItem[it.ID]
		if a == nil {
			// Fallback cost is per STOCK unit (units.CostPerBaseUnit), the same rule stock
			// movements are valued with, so the report and the ledger agree.
			cost := 0.0
			if it.CostPrice != nil {
				unit := ""
				if it.Edges.Units != nil {
					unit = it.Edges.Units.Abbreviation
				}
				cost = units.CostPerBaseUnit(*it.CostPrice, it.PurchasePrice, it.PurchasePackSize, it.YieldPct, unit)
			}
			cn := ""
			if it.CategoryID != nil {
				cn = catName[*it.CategoryID]
			}
			bn := ""
			if it.BrandID != nil {
				bn = brandName[*it.BrandID]
			}
			a = &agg{cost: cost, sku: it.Sku, name: it.Name, cat: cn, catID: it.CategoryID, brand: bn, brandID: it.BrandID}
			perItem[it.ID] = a
		}
		a.onHand += b.OnHand
	}

	val := &StockValuation{Currency: "KES", Offset: offset, Limit: limit, ByBrand: []StockValuationBrand{}}
	catAgg := make(map[string]*StockValuationCategory)
	brandAgg := make(map[string]*StockValuationBrand)
	items := make([]StockValuationItem, 0, len(perItem))
	for id, a := range perItem {
		value := a.onHand * a.cost
		unitCost := a.cost
		costBasis := "item_cost_fallback"
		if la := layerTotals[id]; la != nil && la.qty > 0 {
			value = la.value
			unitCost = money.Round2(la.value / la.qty)
			costBasis = "layers"
		}
		val.TotalValue += value
		val.TotalUnits += a.onHand
		val.ItemCount++

		cn := a.cat
		if cn == "" {
			cn = "Uncategorized"
		}
		c := catAgg[cn]
		if c == nil {
			c = &StockValuationCategory{CategoryName: cn, CategoryID: a.catID}
			catAgg[cn] = c
		}
		c.ItemCount++
		c.TotalUnits += a.onHand
		c.TotalValue += value

		bn := a.brand
		if bn == "" {
			bn = "Unbranded"
		}
		b := brandAgg[bn]
		if b == nil {
			b = &StockValuationBrand{BrandName: bn, BrandID: a.brandID}
			brandAgg[bn] = b
		}
		b.ItemCount++
		b.TotalUnits += a.onHand
		b.TotalValue += value

		items = append(items, StockValuationItem{
			ItemID: id, SKU: a.sku, Name: a.name, CategoryName: cn, BrandName: a.brand,
			OnHand: a.onHand, UnitCost: unitCost, Value: value, CostBasis: costBasis,
		})
	}

	for _, c := range catAgg {
		val.ByCategory = append(val.ByCategory, *c)
	}
	sort.Slice(val.ByCategory, func(i, j int) bool { return val.ByCategory[i].TotalValue > val.ByCategory[j].TotalValue })
	for _, b := range brandAgg {
		val.ByBrand = append(val.ByBrand, *b)
	}
	sort.Slice(val.ByBrand, func(i, j int) bool { return val.ByBrand[i].TotalValue > val.ByBrand[j].TotalValue })

	sort.Slice(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].ItemID.String() < items[j].ItemID.String()
	})
	if offset >= len(items) {
		items = items[:0]
	} else {
		items = items[offset:min(offset+limit, len(items))]
	}
	val.TopItems = items

	return val, nil
}
