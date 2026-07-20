package stock

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	entgoodsreceipt "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgoodsreceiptline "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entstocktransfer "github.com/bengobox/inventory-service/internal/ent/stocktransfer"
	entstocktransferline "github.com/bengobox/inventory-service/internal/ent/stocktransferline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Product Stock History — the Go-Digital-style per-item ledger: aggregate
// quantities-in/quantities-out cards plus a unified movement table.
//
// The ledger is a READ-TIME union of the four movement sources (none of which
// duplicate each other — verified: transfers/receipts/consumption mutate
// InventoryBalance directly and do NOT write StockAdjustment rows):
//   - StockAdjustment   — manual adjustments, opening balances, breakdowns,
//     returns, count variances (carries quantity_before/after + actor).
//   - GoodsReceiptLine  — purchases in (POSTED receipts only).
//   - StockTransferLine — transfers between warehouses (out at shipped_at from
//     the source, in at received_at into the destination).
//   - ConsumptionLine   — sales depletion (POS/ordering BOM path); reversal
//     rows (reason "reversal" / negative qty) surface as sell returns. Rows
//     flagged `theoretical` never moved stock and are EXCLUDED.

// MovementRow is one unified stock-history ledger entry.
type MovementRow struct {
	// Type: opening_stock | purchase | sale | sell_return | purchase_return |
	// transfer_in | transfer_out | adjustment.
	Type           string     `json:"type"`
	// Human label, e.g. "Adjustment (damaged)".
	Label          string     `json:"label"`
	QuantityChange float64    `json:"quantity_change"`
	// Stock level after the movement — known only for StockAdjustment rows.
	QuantityAfter *float64   `json:"quantity_after,omitempty"`
	OccurredAt    time.Time  `json:"occurred_at"`
	Reference     string     `json:"reference,omitempty"`
	WarehouseID   *uuid.UUID `json:"warehouse_id,omitempty"`
	WarehouseName string     `json:"warehouse_name,omitempty"`
	// ActorID is the adjusting/receiving/initiating user when recorded.
	ActorID      *uuid.UUID `json:"actor_id,omitempty"`
	// Counterparty: supplier name (purchases) or order reference (sales).
	Counterparty string `json:"counterparty,omitempty"`
}

// StockHistorySummary mirrors the Go-Digital quantities-in/out cards.
type StockHistorySummary struct {
	OpeningStock         float64 `json:"opening_stock"`
	TotalPurchased       float64 `json:"total_purchased"`
	TotalSellReturns     float64 `json:"total_sell_returns"`
	TransfersIn          float64 `json:"transfers_in"`
	TotalSold            float64 `json:"total_sold"`
	TotalPurchaseReturns float64 `json:"total_purchase_returns"`
	TransfersOut         float64 `json:"transfers_out"`
	// Net of miscellaneous adjustments (damage, shrinkage, found, corrections…).
	TotalAdjusted float64 `json:"total_adjusted"`
	CurrentStock  float64 `json:"current_stock"`
}

// StockHistoryItem identifies the item the history belongs to.
type StockHistoryItem struct {
	ID               uuid.UUID `json:"id"`
	Sku              string    `json:"sku"`
	Name             string    `json:"name"`
	UnitAbbreviation string    `json:"unit_abbreviation,omitempty"`
}

// StockHistoryResult is the endpoint payload: summary + paginated movements.
type StockHistoryResult struct {
	Item      StockHistoryItem    `json:"item"`
	Summary   StockHistorySummary `json:"summary"`
	Movements []MovementRow       `json:"movements"`
	Total     int                 `json:"total"`
}

// StockHistoryFilter narrows the ledger.
type StockHistoryFilter struct {
	WarehouseID *uuid.UUID
	DateFrom    *time.Time
	DateTo      *time.Time
	Limit       int
	Offset      int
}

// perSourceCap bounds each source query — history is per-item so row counts are
// modest; the cap only guards a pathological tenant from an unbounded merge.
const perSourceCap = 5000

// classifyAdjustment maps a StockAdjustment reason (+sign) to a movement type.
// Kept pure for unit tests.
func classifyAdjustment(reason string, change float64) (mvType, label string) {
	switch reason {
	case "opening_balance", "initial_count":
		return "opening_stock", "Opening Stock"
	case "transfer_in":
		return "transfer_in", "Transfer In"
	case "transfer_out":
		return "transfer_out", "Transfer Out"
	case "return":
		// Positive = customer/sell return coming back in; negative = stock
		// leaving for a supplier/purchase return.
		if change >= 0 {
			return "sell_return", "Sell Return"
		}
		return "purchase_return", "Purchase Return"
	}
	return "adjustment", "Adjustment (" + reason + ")"
}

// applyToSummary folds one movement into the aggregate cards. Pure for tests.
func applyToSummary(sum *StockHistorySummary, mvType string, change float64) {
	switch mvType {
	case "opening_stock":
		sum.OpeningStock += change
	case "purchase":
		sum.TotalPurchased += change
	case "sale":
		sum.TotalSold += -change // stored negative (stock out); report positive
	case "sell_return":
		sum.TotalSellReturns += change
	case "purchase_return":
		sum.TotalPurchaseReturns += -change
	case "transfer_in":
		sum.TransfersIn += change
	case "transfer_out":
		sum.TransfersOut += -change
	case "adjustment":
		sum.TotalAdjusted += change
	}
}

// inRange applies the optional date window.
func (f StockHistoryFilter) inRange(t time.Time) bool {
	if f.DateFrom != nil && t.Before(*f.DateFrom) {
		return false
	}
	if f.DateTo != nil && t.After(*f.DateTo) {
		return false
	}
	return true
}

// ItemStockHistory builds the unified per-item ledger + summary.
func (s *Service) ItemStockHistory(ctx context.Context, tenantID uuid.UUID, sku string, f StockHistoryFilter) (*StockHistoryResult, error) {
	it, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.Sku(sku)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("stock: item not found")
		}
		return nil, fmt.Errorf("stock: history item lookup: %w", err)
	}

	res := &StockHistoryResult{
		Item:      StockHistoryItem{ID: it.ID, Sku: it.Sku, Name: it.Name},
		Movements: []MovementRow{},
	}
	if it.UnitID != nil {
		if u, uErr := s.client.Unit.Query().Where(unit.ID(*it.UnitID)).Only(ctx); uErr == nil {
			res.Item.UnitAbbreviation = u.Abbreviation
		}
	}

	var rows []MovementRow

	// 1) StockAdjustment — adjustments/opening/breakdowns/returns.
	adjQ := s.client.StockAdjustment.Query().
		Where(stockadjustment.TenantID(tenantID), stockadjustment.ItemID(it.ID))
	if f.WarehouseID != nil {
		adjQ = adjQ.Where(stockadjustment.WarehouseID(*f.WarehouseID))
	}
	adjs, err := adjQ.Order(ent.Desc(stockadjustment.FieldAdjustedAt)).Limit(perSourceCap).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: history adjustments: %w", err)
	}
	for _, a := range adjs {
		if !f.inRange(a.AdjustedAt) {
			continue
		}
		mvType, label := classifyAdjustment(string(a.Reason), a.QuantityChange)
		after := a.QuantityAfter
		actor := a.AdjustedBy
		wid := a.WarehouseID
		rows = append(rows, MovementRow{
			Type: mvType, Label: label,
			QuantityChange: a.QuantityChange, QuantityAfter: &after,
			OccurredAt: a.AdjustedAt, Reference: a.Reference,
			WarehouseID: &wid, ActorID: &actor,
		})
	}

	// 2) GoodsReceiptLine — purchases in (POSTED receipts only).
	grlQ := s.client.GoodsReceiptLine.Query().
		Where(entgoodsreceiptline.TenantID(tenantID), entgoodsreceiptline.ItemID(it.ID))
	grls, err := grlQ.Limit(perSourceCap).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: history receipts: %w", err)
	}
	if len(grls) > 0 {
		grIDs := make([]uuid.UUID, 0, len(grls))
		for _, l := range grls {
			grIDs = append(grIDs, l.GoodsReceiptID)
		}
		receipts, rErr := s.client.GoodsReceipt.Query().
			Where(entgoodsreceipt.TenantID(tenantID), entgoodsreceipt.IDIn(grIDs...), entgoodsreceipt.StatusEQ("posted")).
			All(ctx)
		if rErr != nil {
			return nil, fmt.Errorf("stock: history receipt heads: %w", rErr)
		}
		recByID := make(map[uuid.UUID]*ent.GoodsReceipt, len(receipts))
		supplierIDs := make([]uuid.UUID, 0)
		for _, r := range receipts {
			recByID[r.ID] = r
			if r.SupplierID != nil {
				supplierIDs = append(supplierIDs, *r.SupplierID)
			}
		}
		supplierNames := map[uuid.UUID]string{}
		if len(supplierIDs) > 0 {
			if sups, sErr := s.client.Supplier.Query().Where(entsupplier.IDIn(supplierIDs...)).All(ctx); sErr == nil {
				for _, sp := range sups {
					supplierNames[sp.ID] = sp.Name
				}
			}
		}
		for _, l := range grls {
			rec, ok := recByID[l.GoodsReceiptID]
			if !ok { // draft/cancelled receipt — no stock moved
				continue
			}
			if f.WarehouseID != nil && (rec.WarehouseID == nil || *rec.WarehouseID != *f.WarehouseID) {
				continue
			}
			if !f.inRange(rec.ReceivedDate) || l.QuantityAccepted == 0 {
				continue
			}
			counterparty := ""
			if rec.SupplierID != nil {
				counterparty = supplierNames[*rec.SupplierID]
			}
			rows = append(rows, MovementRow{
				Type: "purchase", Label: "Purchase (GRN)",
				QuantityChange: l.QuantityAccepted,
				OccurredAt:     rec.ReceivedDate, Reference: rec.GrnNumber,
				WarehouseID: rec.WarehouseID, ActorID: rec.ReceivedBy,
				Counterparty: counterparty,
			})
		}
	}

	// 3) StockTransferLine — out at shipped (in_transit/received), in at received.
	stlQ := s.client.StockTransferLine.Query().
		Where(entstocktransferline.ItemID(it.ID))
	stls, err := stlQ.Limit(perSourceCap).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: history transfer lines: %w", err)
	}
	if len(stls) > 0 {
		trIDs := make([]uuid.UUID, 0, len(stls))
		for _, l := range stls {
			trIDs = append(trIDs, l.TransferID)
		}
		transfers, tErr := s.client.StockTransfer.Query().
			Where(entstocktransfer.TenantID(tenantID), entstocktransfer.IDIn(trIDs...)).
			All(ctx)
		if tErr != nil {
			return nil, fmt.Errorf("stock: history transfer heads: %w", tErr)
		}
		trByID := make(map[uuid.UUID]*ent.StockTransfer, len(transfers))
		for _, t := range transfers {
			trByID[t.ID] = t
		}
		for _, l := range stls {
			tr, ok := trByID[l.TransferID]
			if !ok || tr.Status == "draft" || tr.Status == "cancelled" {
				continue
			}
			// OUT leg: stock left the source when shipped.
			if tr.ShippedAt != nil && f.inRange(*tr.ShippedAt) &&
				(f.WarehouseID == nil || tr.SourceWarehouseID == *f.WarehouseID) {
				srcID := tr.SourceWarehouseID
				rows = append(rows, MovementRow{
					Type: "transfer_out", Label: "Transfer Out",
					QuantityChange: -l.Quantity,
					OccurredAt:     *tr.ShippedAt, Reference: tr.TransferNumber,
					WarehouseID: &srcID, ActorID: tr.InitiatedBy,
				})
			}
			// IN leg: stock arrived at the destination when received.
			if tr.Status == "received" && tr.ReceivedAt != nil && f.inRange(*tr.ReceivedAt) &&
				(f.WarehouseID == nil || tr.DestinationWarehouseID == *f.WarehouseID) {
				dstID := tr.DestinationWarehouseID
				rows = append(rows, MovementRow{
					Type: "transfer_in", Label: "Transfer In",
					QuantityChange: l.Quantity,
					OccurredAt:     *tr.ReceivedAt, Reference: tr.TransferNumber,
					WarehouseID: &dstID, ActorID: tr.InitiatedBy,
				})
			}
		}
	}

	// 4) ConsumptionLine — sales out; reversals in. Theoretical rows never moved stock.
	clQ := s.client.ConsumptionLine.Query().
		Where(
			entconsumptionline.TenantID(tenantID),
			entconsumptionline.IngredientItemID(it.ID),
			entconsumptionline.Theoretical(false),
		)
	if f.WarehouseID != nil {
		clQ = clQ.Where(entconsumptionline.WarehouseID(*f.WarehouseID))
	}
	cls, err := clQ.Order(ent.Desc(entconsumptionline.FieldConsumedAt)).Limit(perSourceCap).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: history consumption: %w", err)
	}
	for _, c := range cls {
		if !f.inRange(c.ConsumedAt) || c.Quantity == 0 {
			continue
		}
		wid := c.WarehouseID
		ref := c.FinishedItemSku
		counterparty := "Order " + shortID(c.OrderID)
		if c.Reason == reversalReason || c.Quantity < 0 {
			qty := c.Quantity
			if qty < 0 {
				qty = -qty
			}
			rows = append(rows, MovementRow{
				Type: "sell_return", Label: "Sell Return / Reversal",
				QuantityChange: qty, OccurredAt: c.ConsumedAt,
				Reference: ref, WarehouseID: wid, Counterparty: counterparty,
			})
			continue
		}
		rows = append(rows, MovementRow{
			Type: "sale", Label: "Sold",
			QuantityChange: -c.Quantity, OccurredAt: c.ConsumedAt,
			Reference: ref, WarehouseID: wid, Counterparty: counterparty,
		})
	}

	// Summary over the FULL filtered range (not just the page).
	for _, r := range rows {
		applyToSummary(&res.Summary, r.Type, r.QuantityChange)
	}

	// Current stock: live balance rows (warehouse-scoped when filtered).
	balQ := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.ItemID(it.ID))
	if f.WarehouseID != nil {
		balQ = balQ.Where(inventorybalance.WarehouseID(*f.WarehouseID))
	}
	if bals, bErr := balQ.All(ctx); bErr == nil {
		for _, b := range bals {
			res.Summary.CurrentStock += b.OnHand
		}
	}

	// Resolve warehouse names once.
	whIDs := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		if r.WarehouseID != nil {
			whIDs[*r.WarehouseID] = struct{}{}
		}
	}
	if len(whIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(whIDs))
		for id := range whIDs {
			ids = append(ids, id)
		}
		if whs, wErr := s.client.Warehouse.Query().Where(warehouse.IDIn(ids...)).All(ctx); wErr == nil {
			names := make(map[uuid.UUID]string, len(whs))
			for _, w := range whs {
				names[w.ID] = w.Name
			}
			for i := range rows {
				if rows[i].WarehouseID != nil {
					rows[i].WarehouseName = names[*rows[i].WarehouseID]
				}
			}
		}
	}

	// Newest first, then stable page slice.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].OccurredAt.After(rows[j].OccurredAt) })
	res.Total = len(rows)
	start := f.Offset
	if start > len(rows) {
		start = len(rows)
	}
	end := len(rows)
	if f.Limit > 0 && start+f.Limit < end {
		end = start + f.Limit
	}
	res.Movements = rows[start:end]
	return res, nil
}

// shortID renders the first uuid segment for a compact order reference.
func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}
