// Package reports computes cross-domain (stock + recipe) reporting views that don't belong
// to any single owning module. IngredientUtilization answers the "how much of ingredient X
// went into which recipe, over what period, relative to its reorder level" question — see
// D:\Projects\Codevertex\.claude\plans\ingredient-utilization-reorder-report-2026-07-10.md.
package reports

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	"github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stocklevelevent"
)

// Service computes ingredient-utilization report views on top of ConsumptionLine,
// ItemConsumptionDaily, StockLevelEvent, GoodsReceiptLine and InventoryBalance — every one
// of those tables already belongs to inventory-api, so this is a read-side composition, not
// a new data owner. warehouseID is required (not "all warehouses"): reorder-level phase
// banding is inherently a per-warehouse threshold, so a single scope keeps the numbers
// meaningful — matches resolveWarehouseID's default-warehouse convention in stock.Service.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new ingredient-utilization reports Service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{client: client, log: log.Named("reports.ingredient_utilization")}
}

// IngredientUtilizationSummary is the KPI-tile payload for one ingredient over a period.
type IngredientUtilizationSummary struct {
	ItemID               uuid.UUID  `json:"item_id"`
	ItemSKU              string     `json:"item_sku"`
	ItemName             string     `json:"item_name"`
	Unit                 string     `json:"unit,omitempty"`
	WarehouseID          uuid.UUID  `json:"warehouse_id"`
	PeriodStart          time.Time  `json:"period_start"`
	PeriodEnd            time.Time  `json:"period_end"`
	PurchasedQty         float64    `json:"purchased_qty"`
	PurchasedCost        float64    `json:"purchased_cost"`
	ConsumedQty          float64    `json:"consumed_qty"`
	ConsumedCost         float64    `json:"consumed_cost"`
	OnHand               float64    `json:"on_hand"`
	Available            float64    `json:"available"`
	ReorderLevel         int        `json:"reorder_level"`
	DailyVelocity        float64    `json:"daily_velocity" comment:"average consumed_qty per day over the period"`
	ProjectedDaysOfCover *float64   `json:"projected_days_of_cover,omitempty" comment:"available / daily_velocity; omitted when velocity is zero"`
	LastRestockAt        *time.Time `json:"last_restock_at,omitempty"`
}

// GetSummary computes the KPI tiles for one ingredient item over [from, to] at one warehouse.
func (s *Service) GetSummary(ctx context.Context, tenantID, itemID, warehouseID uuid.UUID, from, to time.Time) (*IngredientUtilizationSummary, error) {
	itm, err := s.client.Item.Query().
		Where(item.ID(itemID), item.TenantID(tenantID)).
		WithUnits().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("reports: load item: %w", err)
	}

	summary := &IngredientUtilizationSummary{
		ItemID:      itemID,
		ItemSKU:     itm.Sku,
		ItemName:    itm.Name,
		WarehouseID: warehouseID,
		PeriodStart: from,
		PeriodEnd:   to,
	}
	if itm.Edges.Units != nil {
		summary.Unit = itm.Edges.Units.Abbreviation
	}

	var consumedAgg []struct {
		Qty  float64 `json:"sum_quantity"`
		Cost float64 `json:"sum_total_cost"`
	}
	if err := s.client.ConsumptionLine.Query().
		Where(
			entconsumptionline.TenantID(tenantID),
			entconsumptionline.IngredientItemID(itemID),
			entconsumptionline.WarehouseID(warehouseID),
			entconsumptionline.Theoretical(false),
			entconsumptionline.ConsumedAtGTE(from),
			entconsumptionline.ConsumedAtLTE(to),
		).
		Aggregate(ent.Sum(entconsumptionline.FieldQuantity), ent.Sum(entconsumptionline.FieldTotalCost)).
		Scan(ctx, &consumedAgg); err != nil {
		return nil, fmt.Errorf("reports: sum consumption: %w", err)
	}
	if len(consumedAgg) > 0 {
		summary.ConsumedQty = consumedAgg[0].Qty
		summary.ConsumedCost = consumedAgg[0].Cost
	}

	// GoodsReceiptLine has no warehouse_id of its own (it belongs to a GoodsReceipt that
	// does) — purchased totals are tenant+item scoped across all warehouses. Volumes per
	// item per period are small, so summing in Go avoids an extra join for one report.
	receiptLines, err := s.client.GoodsReceiptLine.Query().
		Where(
			goodsreceiptline.TenantID(tenantID),
			goodsreceiptline.ItemID(itemID),
			goodsreceiptline.CreatedAtGTE(from),
			goodsreceiptline.CreatedAtLTE(to),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("reports: load goods receipt lines: %w", err)
	}
	for _, l := range receiptLines {
		summary.PurchasedQty += l.QuantityAccepted
		summary.PurchasedCost += l.QuantityAccepted * l.UnitCost
	}

	if bal, berr := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itemID),
			inventorybalance.WarehouseID(warehouseID),
		).
		First(ctx); berr == nil {
		summary.OnHand = bal.OnHand
		summary.Available = bal.Available
		summary.ReorderLevel = bal.ReorderLevel
	}

	days := to.Sub(from).Hours() / 24
	if days > 0 {
		summary.DailyVelocity = round4(summary.ConsumedQty / days)
	}
	if summary.DailyVelocity > 0 {
		cover := round4(summary.Available / summary.DailyVelocity)
		summary.ProjectedDaysOfCover = &cover
	}

	if lastRestock, rerr := s.client.StockLevelEvent.Query().
		Where(
			stocklevelevent.TenantID(tenantID),
			stocklevelevent.ItemID(itemID),
			stocklevelevent.WarehouseID(warehouseID),
			stocklevelevent.EventTypeEQ(stocklevelevent.EventTypeRestocked),
		).
		Order(ent.Desc(stocklevelevent.FieldOccurredAt)).
		First(ctx); rerr == nil {
		summary.LastRestockAt = &lastRestock.OccurredAt
	}

	return summary, nil
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
