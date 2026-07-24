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

// FastMovingItem is a single high-turnover stock line.
type FastMovingItem struct {
	ItemID        uuid.UUID `json:"item_id"`
	SKU           string    `json:"sku"`
	Name          string    `json:"name"`
	CategoryName  string    `json:"category_name,omitempty"`
	OnHand        float64   `json:"on_hand"`
	QuantityMoved float64   `json:"quantity_moved"`
	LastActivity  time.Time `json:"last_activity"`
}

// FastMovingReport ranks items by outbound movement volume within the lookback window — the
// mirror image of DeadstockReport, using the same usageReasons movement definition so the two
// reports agree on what counts as "moved".
type FastMovingReport struct {
	Days      int              `json:"days"`
	ItemCount int              `json:"item_count"`
	Items     []FastMovingItem `json:"items"`
}

// StockFastMoving ranks on-hand items by outbound movement quantity (see usageReasons) within the
// last `days` days — the busiest SKUs by volume, descending. Returns the top 100 items.
func (s *Service) StockFastMoving(ctx context.Context, tenantID uuid.UUID, days int) (*FastMovingReport, error) {
	if days <= 0 {
		days = 90
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	cons, err := s.client.Consumption.Query().
		Where(consumption.TenantID(tenantID), consumption.ReasonIn(usageReasons...), consumption.CreatedAtGTE(cutoff)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("fastmoving: query consumptions: %w", err)
	}

	movedQty := make(map[string]float64) // SKU → total quantity moved within the window
	lastMovedBySKU := make(map[string]time.Time)
	for _, c := range cons {
		for _, it := range c.Items {
			sku := strings.ToUpper(strings.TrimSpace(it.SKU))
			if sku == "" {
				continue
			}
			movedQty[sku] += it.Quantity
			if c.CreatedAt.After(lastMovedBySKU[sku]) {
				lastMovedBySKU[sku] = c.CreatedAt
			}
		}
	}
	if len(movedQty) == 0 {
		return &FastMovingReport{Days: days}, nil
	}

	balances, err := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID)).
		WithItem().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("fastmoving: query balances: %w", err)
	}

	cats, _ := s.client.ItemCategory.Query().Where(itemcategory.TenantID(tenantID)).All(ctx)
	catName := make(map[uuid.UUID]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = c.Name
	}

	type agg struct {
		onHand    float64
		sku, name string
		cat       string
		last      time.Time
	}
	per := make(map[uuid.UUID]*agg)
	for _, b := range balances {
		it := b.Edges.Item
		if it == nil {
			continue
		}
		skuKey := strings.ToUpper(strings.TrimSpace(it.Sku))
		if _, moved := movedQty[skuKey]; !moved {
			continue
		}
		a := per[it.ID]
		if a == nil {
			cn := ""
			if it.CategoryID != nil {
				cn = catName[*it.CategoryID]
			}
			a = &agg{sku: it.Sku, name: it.Name, cat: cn, last: lastMovedBySKU[skuKey]}
			per[it.ID] = a
		}
		a.onHand += b.OnHand
	}

	rep := &FastMovingReport{Days: days}
	for id, a := range per {
		rep.ItemCount++
		rep.Items = append(rep.Items, FastMovingItem{
			ItemID: id, SKU: a.sku, Name: a.name, CategoryName: a.cat,
			OnHand: a.onHand, QuantityMoved: movedQty[strings.ToUpper(strings.TrimSpace(a.sku))], LastActivity: a.last,
		})
	}
	sort.Slice(rep.Items, func(i, j int) bool { return rep.Items[i].QuantityMoved > rep.Items[j].QuantityMoved })
	if len(rep.Items) > 100 {
		rep.Items = rep.Items[:100]
	}
	return rep, nil
}
