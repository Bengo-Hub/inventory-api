package items

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
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
	TopItems   []StockValuationItem     `json:"top_items"`
}

// StockValuation computes each item's inventory value from its own InventoryLot cost layers
// (Σ layer.quantity × layer.cost_price) — what was actually paid for the stock on hand, never a
// single mutable Item.cost_price retroactively applied to everything. Items with no active cost
// layer yet (pre-cutover stock awaiting the opening-balance backfill) fall back to
// on_hand × the item's standard cost, and are flagged via CostBasis so the gap is visible on the
// report rather than silently blended in. Returns the grand total, a per-category breakdown, and
// the top-20 items by value.
func (s *Service) StockValuation(ctx context.Context, tenantID uuid.UUID) (*StockValuation, error) {
	balances, err := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID)).
		WithItem().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock valuation: query balances: %w", err)
	}

	cats, _ := s.client.ItemCategory.Query().Where(itemcategory.TenantID(tenantID)).All(ctx)
	catName := make(map[uuid.UUID]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = c.Name
	}

	// Per-item layer totals: quantity + value across every active, cost-recorded lot (real lots
	// AND internal cost layers — same table), regardless of warehouse, matching the balance
	// aggregation below (this report is tenant-wide, not per-warehouse).
	layers, err := s.client.InventoryLot.Query().
		Where(entlot.TenantID(tenantID), entlot.StatusEQ(entlot.StatusActive), entlot.CostPriceNotNil()).
		All(ctx)
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
		cat          string
		catID        *uuid.UUID
	}
	perItem := make(map[uuid.UUID]*agg)
	for _, b := range balances {
		it := b.Edges.Item
		if it == nil {
			continue
		}
		a := perItem[it.ID]
		if a == nil {
			cost := 0.0
			if it.CostPrice != nil {
				cost = *it.CostPrice
			}
			cn := ""
			if it.CategoryID != nil {
				cn = catName[*it.CategoryID]
			}
			a = &agg{cost: cost, sku: it.Sku, name: it.Name, cat: cn, catID: it.CategoryID}
			perItem[it.ID] = a
		}
		a.onHand += b.OnHand
	}

	val := &StockValuation{Currency: "KES"}
	catAgg := make(map[string]*StockValuationCategory)
	items := make([]StockValuationItem, 0, len(perItem))
	for id, a := range perItem {
		value := a.onHand * a.cost
		unitCost := a.cost
		costBasis := "item_cost_fallback"
		if la := layerTotals[id]; la != nil && la.qty > 0 {
			value = la.value
			unitCost = round2(la.value / la.qty)
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

		items = append(items, StockValuationItem{
			ItemID: id, SKU: a.sku, Name: a.name, CategoryName: cn,
			OnHand: a.onHand, UnitCost: unitCost, Value: value, CostBasis: costBasis,
		})
	}

	for _, c := range catAgg {
		val.ByCategory = append(val.ByCategory, *c)
	}
	sort.Slice(val.ByCategory, func(i, j int) bool { return val.ByCategory[i].TotalValue > val.ByCategory[j].TotalValue })

	sort.Slice(items, func(i, j int) bool { return items[i].Value > items[j].Value })
	if len(items) > 20 {
		items = items[:20]
	}
	val.TopItems = items

	return val, nil
}
