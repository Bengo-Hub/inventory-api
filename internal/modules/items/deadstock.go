package items

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent/consumption"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
)

// DeadstockItem is a single non-moving stock line.
type DeadstockItem struct {
	ItemID       uuid.UUID `json:"item_id"`
	SKU          string    `json:"sku"`
	Name         string    `json:"name"`
	CategoryName string    `json:"category_name,omitempty"`
	OnHand       float64   `json:"on_hand"`
	UnitCost     float64   `json:"unit_cost"`
	Value        float64   `json:"value"`
	LastActivity time.Time `json:"last_activity"`
}

// DeadstockReport lists capital tied up in stock that has not sold within the lookback window.
type DeadstockReport struct {
	Days           int             `json:"days"`
	Currency       string          `json:"currency"`
	ItemCount      int             `json:"item_count"`
	TotalDeadValue float64         `json:"total_dead_value"`
	Items          []DeadstockItem `json:"items"`
}

// StockDeadstock lists on-hand items whose SKU has NOT been sold (consumption reason="sale") within
// the last `days` days — i.e. capital tied up in non-moving stock. Value = on_hand × cost_price.
// Returns the top 100 items by tied-up value.
func (s *Service) StockDeadstock(ctx context.Context, tenantID uuid.UUID, days int) (*DeadstockReport, error) {
	if days <= 0 {
		days = 90
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	// Sale-type consumptions count as "movement". The pos→inventory backflush records reason
	// "pos_sale"; other flows may use "sale" — match both so deadstock means genuinely not selling.
	cons, err := s.client.Consumption.Query().
		Where(consumption.TenantID(tenantID), consumption.ReasonIn("sale", "pos_sale"), consumption.CreatedAtGTE(cutoff)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("deadstock: query consumptions: %w", err)
	}
	soldRecently := make(map[string]bool)
	for _, c := range cons {
		for _, it := range c.Items {
			soldRecently[strings.ToUpper(strings.TrimSpace(it.SKU))] = true
		}
	}

	balances, err := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.OnHandGT(0)).
		WithItem().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("deadstock: query balances: %w", err)
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
		last         time.Time
	}
	per := make(map[uuid.UUID]*agg)
	for _, b := range balances {
		it := b.Edges.Item
		if it == nil {
			continue
		}
		if soldRecently[strings.ToUpper(it.Sku)] {
			continue // sold within the window — not deadstock
		}
		a := per[it.ID]
		if a == nil {
			cost := 0.0
			if it.CostPrice != nil {
				cost = *it.CostPrice
			}
			cn := ""
			if it.CategoryID != nil {
				cn = catName[*it.CategoryID]
			}
			a = &agg{cost: cost, sku: it.Sku, name: it.Name, cat: cn, last: b.UpdatedAt}
			per[it.ID] = a
		}
		a.onHand += b.OnHand
		if b.UpdatedAt.After(a.last) {
			a.last = b.UpdatedAt
		}
	}

	rep := &DeadstockReport{Days: days, Currency: "KES"}
	for id, a := range per {
		val := a.onHand * a.cost
		rep.TotalDeadValue += val
		rep.ItemCount++
		rep.Items = append(rep.Items, DeadstockItem{
			ItemID: id, SKU: a.sku, Name: a.name, CategoryName: a.cat,
			OnHand: a.onHand, UnitCost: a.cost, Value: val, LastActivity: a.last,
		})
	}
	sort.Slice(rep.Items, func(i, j int) bool { return rep.Items[i].Value > rep.Items[j].Value })
	if len(rep.Items) > 100 {
		rep.Items = rep.Items[:100]
	}
	return rep, nil
}
