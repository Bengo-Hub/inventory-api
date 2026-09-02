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
	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
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
//     the source, in at received_at into the destination); a transfer that was
//     explicitly back/post-dated via transfer_date at entry shows that date
//     instead (see ledgerMovementDate) — the real shipped_at/received_at is
//     still carried on the row as EnteredAt, only the primary ledger date and
//     the date-range filter honor the override. The delivery-note/GRN PDF
//     documents deliberately keep printing the raw timestamps regardless
//     (transfers_documents.go's effectiveTransferDisplayDate doc comment) —
//     those are the shipment/receipt paperwork itself, not a reporting view.
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
	// ActorID is the adjusting/receiving/initiating/serving user when recorded.
	ActorID      *uuid.UUID `json:"actor_id,omitempty"`
	// ActorName is ActorID resolved to a display name — who performed this movement,
	// surfaced so admins/managers can audit which user did what on critical transactions.
	ActorName string `json:"actor_name,omitempty"`
	// Counterparty: supplier name (purchases) or customer name (sales/sell returns).
	Counterparty string `json:"counterparty,omitempty"`
	// EnteredAt is the real ship/receive event timestamp, present only when OccurredAt was
	// overridden away from it by a transfer's transfer_date (see ledgerMovementDate) — never
	// hides the real audit timestamp, just stops it from being the misleading headline date.
	EnteredAt *time.Time `json:"entered_at,omitempty"`
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
	// Types, when non-empty, restricts the returned ledger ROWS to these movement types
	// (see MovementRow.Type for the vocabulary). Applied after the summary cards are
	// computed, so narrowing the table view never changes the aggregate totals.
	Types  []string
	Limit  int
	Offset int
}

// matchesType reports whether mvType passes the optional type filter (empty = match all).
func (f StockHistoryFilter) matchesType(mvType string) bool {
	if len(f.Types) == 0 {
		return true
	}
	for _, t := range f.Types {
		if t == mvType {
			return true
		}
	}
	return false
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
	case "location_hidden":
		return "adjustment", "Hidden from Outlet"
	case "location_unhidden":
		return "adjustment", "Unhidden — Stock Restored"
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

// backfillQuantityAfter fills the QuantityAfter of any row that doesn't already carry one
// (purchases, transfers, sales, sell-returns — only StockAdjustment rows record a real
// snapshot at write time). rows MUST already be sorted newest-first. Walks backward from
// currentStock, treating each row's stored QuantityAfter (when present) as an authoritative
// anchor — so genuine adjustment snapshots are never overwritten, only trusted and resynced
// from — and running-balance-filling the gaps in between. Pure for tests.
func backfillQuantityAfter(rows []MovementRow, currentStock float64) {
	after := currentStock
	for i := range rows {
		if rows[i].QuantityAfter != nil {
			after = *rows[i].QuantityAfter
		} else {
			snapshot := after
			rows[i].QuantityAfter = &snapshot
		}
		after -= rows[i].QuantityChange
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

// ledgerMovementDate resolves a transfer leg's ledger date: the transfer_date override
// (transfers.EffectiveTransferDate's precedence) when the tenant explicitly back/post-dated the
// whole transfer at entry, else real unchanged (the raw shipped_at/received_at). enteredAt
// carries the real event timestamp whenever overridden — the ledger's "New quantity" running
// balance still sorts/anchors on OccurredAt, so a backdated transfer correctly lands among its
// chronological neighbors instead of floating under today (same reasoning as
// transfers.orderByEffectiveDate for the Transfers list).
func ledgerMovementDate(tr *ent.StockTransfer, real time.Time) (occurred time.Time, enteredAt *time.Time) {
	if tr.TransferDate == nil {
		return real, nil
	}
	return *tr.TransferDate, &real
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
		poIDs := make([]uuid.UUID, 0)
		for _, r := range receipts {
			recByID[r.ID] = r
			if r.SupplierID != nil {
				supplierIDs = append(supplierIDs, *r.SupplierID)
			}
			if r.PurchaseOrderID != uuid.Nil {
				poIDs = append(poIDs, r.PurchaseOrderID)
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
		// The purchase reference should be the ORIGINATING Purchase Order number (what the
		// buyer actually raised), not the GRN's own receiving-note number — the GRN number is
		// kept only as a fallback for a receipt with no linked PO (a direct/ad-hoc receipt).
		poNumbers := map[uuid.UUID]string{}
		if len(poIDs) > 0 {
			if pos, pErr := s.client.PurchaseOrder.Query().
				Where(entpurchaseorder.IDIn(poIDs...)).
				Select(entpurchaseorder.FieldID, entpurchaseorder.FieldPoNumber).
				All(ctx); pErr == nil {
				for _, po := range pos {
					poNumbers[po.ID] = po.PoNumber
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
			reference := poNumbers[rec.PurchaseOrderID]
			if reference == "" {
				reference = rec.GrnNumber
			}
			rows = append(rows, MovementRow{
				Type: "purchase", Label: "Purchase (GRN)",
				QuantityChange: l.QuantityAccepted,
				OccurredAt:     rec.ReceivedDate, Reference: reference,
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
			if tr.ShippedAt != nil && (f.WarehouseID == nil || tr.SourceWarehouseID == *f.WarehouseID) {
				occurred, enteredAt := ledgerMovementDate(tr, *tr.ShippedAt)
				if f.inRange(occurred) {
					srcID := tr.SourceWarehouseID
					rows = append(rows, MovementRow{
						Type: "transfer_out", Label: "Transfer Out",
						QuantityChange: -l.Quantity,
						OccurredAt:     occurred, EnteredAt: enteredAt, Reference: tr.TransferNumber,
						WarehouseID: &srcID, ActorID: tr.InitiatedBy,
					})
				}
			}
			// IN leg: stock arrived at the destination when received.
			if tr.Status == "received" && tr.ReceivedAt != nil &&
				(f.WarehouseID == nil || tr.DestinationWarehouseID == *f.WarehouseID) {
				occurred, enteredAt := ledgerMovementDate(tr, *tr.ReceivedAt)
				if f.inRange(occurred) {
					dstID := tr.DestinationWarehouseID
					rows = append(rows, MovementRow{
						Type: "transfer_in", Label: "Transfer In",
						QuantityChange: l.Quantity,
						OccurredAt:     occurred, EnteredAt: enteredAt, Reference: tr.TransferNumber,
						WarehouseID: &dstID, ActorID: tr.InitiatedBy,
					})
				}
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
		// The real POS order/receipt number when the sale carried one (denormalized from
		// pos.sale.finalized — see ConsumptionLine schema); a pre-fix historical row (or a
		// non-POS consumer, e.g. ordering-backend) that never carried it falls back to a
		// short order-id reference rather than the item's own SKU, which is what this used
		// to (incorrectly) show for every row here regardless of which order it was.
		reference := c.OrderNumber
		if reference == "" {
			reference = "Order " + shortID(c.OrderID)
		}
		counterparty := c.CustomerName
		if counterparty == "" {
			counterparty = "Walk-in"
		}
		var actorID *uuid.UUID
		if c.ServedByUserID != nil {
			actorID = c.ServedByUserID
		}
		if c.Reason == reversalReason || c.Quantity < 0 {
			qty := c.Quantity
			if qty < 0 {
				qty = -qty
			}
			rows = append(rows, MovementRow{
				Type: "sell_return", Label: "Sell Return / Reversal",
				QuantityChange: qty, OccurredAt: c.ConsumedAt,
				Reference: reference, WarehouseID: wid, Counterparty: counterparty,
				ActorID: actorID, ActorName: c.ServedByName,
			})
			continue
		}
		rows = append(rows, MovementRow{
			Type: "sale", Label: "Sold",
			QuantityChange: -c.Quantity, OccurredAt: c.ConsumedAt,
			Reference: reference, WarehouseID: wid, Counterparty: counterparty,
			ActorID: actorID, ActorName: c.ServedByName,
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

	// Resolve actor names once — every row already carrying a denormalized name (sale/sell
	// return rows carry ConsumptionLine.served_by_name) keeps it; everything else (adjustment
	// adjusted_by, purchase received_by, transfer initiated_by) is batch-resolved here, the one
	// place a raw actor UUID becomes a real "who did this" name for the ledger's User column.
	actorIDs := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		if r.ActorID != nil && r.ActorName == "" {
			actorIDs[*r.ActorID] = struct{}{}
		}
	}
	if len(actorIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(actorIDs))
		for id := range actorIDs {
			ids = append(ids, id)
		}
		if users, uErr := s.client.InventoryUser.Query().
			Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserIDIn(ids...)).
			All(ctx); uErr == nil {
			names := make(map[uuid.UUID]string, len(users))
			for _, u := range users {
				if u.Name != "" {
					names[u.AuthServiceUserID] = u.Name
				} else {
					names[u.AuthServiceUserID] = u.Email
				}
			}
			for i := range rows {
				if rows[i].ActorID != nil && rows[i].ActorName == "" {
					rows[i].ActorName = names[*rows[i].ActorID]
				}
			}
		}
	}

	// Newest first — required before backfillQuantityAfter, which walks the ledger in this
	// order, and done before the type filter so the running balance always reflects the FULL
	// history (a row's "New quantity" must stay correct even when the Type filter hides the
	// other rows around it).
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].OccurredAt.After(rows[j].OccurredAt) })

	// Only StockAdjustment rows record their own QuantityAfter at write time; purchases,
	// transfers, sales and sell-returns leave it nil (the historical "dashes" in the New
	// quantity column). Back-fill those from a running balance anchored on the live current
	// stock and any known adjustment snapshots.
	backfillQuantityAfter(rows, res.Summary.CurrentStock)

	// Movement-type filter narrows the TABLE view only — applied after the summary cards and
	// the running-balance backfill (both above) are computed from the full filtered range, so
	// switching "Type" in the UI never changes the Quantities In/Out totals or the New
	// quantity values.
	if len(f.Types) > 0 {
		filtered := rows[:0]
		for _, r := range rows {
			if f.matchesType(r.Type) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

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
