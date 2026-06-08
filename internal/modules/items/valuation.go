package items

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
)

// StockValuationItem is a single line in the stock valuation (value = on_hand × unit cost).
type StockValuationItem struct {
	ItemID       uuid.UUID `json:"item_id"`
	SKU          string    `json:"sku"`
	Name         string    `json:"name"`
	CategoryName string    `json:"category_name,omitempty"`
	OnHand       float64   `json:"on_hand"`
	UnitCost     float64   `json:"unit_cost"`
	Value        float64   `json:"value"`
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

// StockValuation computes on-hand × cost_price across all warehouses for a tenant, returning the
// grand total, a per-category breakdown, and the top-20 items by value. Items without a cost price
// contribute zero value.
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
			OnHand: a.onHand, UnitCost: a.cost, Value: value,
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
