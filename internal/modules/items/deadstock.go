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

// usageReasons are the consumption reasons that count as genuine outbound MOVEMENT of stock — i.e.
// the item is actually being used up, not sitting idle. This deliberately spans BOTH demand channels:
//   - sale / pos_sale       → finished goods sold to a customer
//   - production            → raw materials / ingredients issued to a manufacturing batch (BOM)
//   - conference_meal       → ingredients drawn down for conference/event meal cards
//
// Raw materials are NEVER "sold", so a sales-only definition wrongly flags every ingredient as
// deadstock even when it is heavily consumed in production. Reasons like opening_balance /
// initial_count (stock-in) and purchase_return (reversal) are intentionally excluded — they are not
// demand-driven usage.
var usageReasons = []string{"sale", "pos_sale", "production", "conference_meal"}

// StockDeadstock lists on-hand items whose SKU has had NO outbound movement (see usageReasons) within
// the last `days` days — i.e. capital tied up in non-moving stock. This counts production/recipe
// consumption as movement, so actively-used raw materials are NOT misreported as deadstock.
// Value = on_hand × cost_price. Returns the top 100 items by tied-up value.
func (s *Service) StockDeadstock(ctx context.Context, tenantID uuid.UUID, days int) (*DeadstockReport, error) {
	if days <= 0 {
		days = 90
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	// Pull usage-type consumptions so we can both gate on the window AND surface a truthful "last used"
	// date for genuinely dead items. Bounded to a 2-year lookback so the scan stays cheap on long-lived
	// tenants — anything not used in 2 years falls back to the balance's last-change date anyway.
	queryFloor := time.Now().AddDate(-2, 0, 0)
	cons, err := s.client.Consumption.Query().
		Where(consumption.TenantID(tenantID), consumption.ReasonIn(usageReasons...), consumption.CreatedAtGTE(queryFloor)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("deadstock: query consumptions: %w", err)
	}
	movedRecently := make(map[string]bool)    // SKU → moved within the window
	lastUsedBySKU := make(map[string]time.Time) // SKU → most recent outbound movement (any time)
	for _, c := range cons {
		for _, it := range c.Items {
			sku := strings.ToUpper(strings.TrimSpace(it.SKU))
			if sku == "" {
				continue
			}
			if c.CreatedAt.After(cutoff) {
				movedRecently[sku] = true
			}
			if c.CreatedAt.After(lastUsedBySKU[sku]) {
				lastUsedBySKU[sku] = c.CreatedAt
			}
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
		skuKey := strings.ToUpper(strings.TrimSpace(it.Sku))
		if movedRecently[skuKey] {
			continue // moved (sold or consumed in production) within the window — not deadstock
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
			// Show the genuine last-usage date when the item has ever moved; fall back to the
			// balance's last change (e.g. last receipt) only when it has never had an outbound movement.
			last := b.UpdatedAt
			if lu, ok := lastUsedBySKU[skuKey]; ok {
				last = lu
			}
			a = &agg{cost: cost, sku: it.Sku, name: it.Name, cat: cn, last: last}
			per[it.ID] = a
		}
		a.onHand += b.OnHand
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
