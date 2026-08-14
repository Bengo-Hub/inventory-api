package stock

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
)

// BulkAdjustLine is one item's adjustment within a bulk request. When DestinationWarehouseID is
// set, Adjustment is treated as the quantity to MOVE from the request's shared WarehouseID to that
// destination (posted as a transfer_out at the source + transfer_in at the destination) instead of
// an in-place add/remove — its magnitude is used regardless of the sign the caller sent.
type BulkAdjustLine struct {
	SKU                    string     `json:"sku"`
	Adjustment             float64    `json:"adjustment"`
	DestinationWarehouseID *uuid.UUID `json:"destination_warehouse_id,omitempty"`
}

// BulkAdjustStockRequest applies many per-item adjustments against ONE shared warehouse/reason —
// the "select rows, pick a warehouse + reason, key in a delta per item, submit once" flow shared
// by the Products, Stock Levels, and Stock Adjustments pages (see BulkAdjustStockDialog on the
// frontend, the single shared component all three reuse).
type BulkAdjustStockRequest struct {
	Lines       []BulkAdjustLine
	Reason      string
	Reference   string
	Notes       string
	AdjustedBy  uuid.UUID
	WarehouseID uuid.UUID
	OutletID    uuid.UUID
}

// BulkAdjustSkipped explains why one line's adjustment wasn't applied.
type BulkAdjustSkipped struct {
	SKU    string `json:"sku"`
	Reason string `json:"reason"`
}

// BulkAdjustStockResult reports per-line outcomes (same shape as items.BulkActionResult).
type BulkAdjustStockResult struct {
	Processed int                 `json:"processed"`
	Skipped   []BulkAdjustSkipped `json:"skipped"`
}

// BulkAdjustStock applies each line through the SAME AdjustStock path a single adjustment uses —
// deliberately not a bespoke bulk mutation — so a bulk adjustment gets identical treatment to a
// one-off one: FIFO cost-layer consumption, GL-postable journal entries, and the low-stock/
// ingredient-restock cascade. Each line runs in ITS OWN transaction (AdjustStock owns its own tx
// internally) — a failure on one line never rolls back lines that already succeeded, and is
// reported per-item rather than aborting the whole batch, matching the items/bulk.go tolerance
// precedent.
//
// KNOWN LIMITATION: unlike the single /adjust endpoint, this does NOT run the manager-approval
// workflow gate (gateStockAdjustment, HTTP-handler-level, tied to http.ResponseWriter for its
// intent/retry flow) — a bulk adjustment applies immediately regardless of amount. Only
// PermStockAdd-holders can reach this endpoint at all (same permission the single endpoint
// requires), but a tenant relying on approval thresholds for large single adjustments does not
// get that same protection here. Flagged, not silently swallowed — integrating the approval
// workflow per-line is a real follow-up, not attempted here under this turn's time constraints.
func (s *Service) BulkAdjustStock(ctx context.Context, tenantID uuid.UUID, req BulkAdjustStockRequest) (*BulkAdjustStockResult, error) {
	res := &BulkAdjustStockResult{Skipped: []BulkAdjustSkipped{}}

	// Batch-resolve SKU -> ItemID once, up front, only for lines that carry a destination
	// warehouse — that's the only case that needs an Item id (for the after-the-fact transfer
	// audit record below). A resolution miss just means that line's record-keeping is skipped;
	// it never blocks the move itself (AdjustStock re-resolves the SKU on its own regardless).
	itemIDBySKU := make(map[string]uuid.UUID)
	{
		var moveSKUs []string
		for _, ln := range req.Lines {
			if ln.DestinationWarehouseID != nil {
				moveSKUs = append(moveSKUs, ln.SKU)
			}
		}
		if len(moveSKUs) > 0 {
			items, iErr := s.client.Item.Query().Where(item.TenantID(tenantID), item.SkuIn(moveSKUs...)).All(ctx)
			if iErr == nil {
				for _, it := range items {
					itemIDBySKU[it.Sku] = it.ID
				}
			}
		}
	}
	// Successfully-moved lines, grouped by destination warehouse below (source is always the
	// single req.WarehouseID) — recorded as a completed StockTransfer AFTER the loop so every
	// warehouse-to-warehouse move made through a bulk adjustment gets a transfer_number and shows
	// up in the Transfers list, same as one created through the New Transfer dialog. Best-effort:
	// the real stock movement already happened by the time this runs, so a recording failure is
	// logged, never surfaced as a failure of the (already-succeeded) move.
	movedByDest := make(map[uuid.UUID][]transfers.CompletedTransferLine)

	for _, line := range req.Lines {
		if line.Adjustment == 0 {
			res.Skipped = append(res.Skipped, BulkAdjustSkipped{SKU: line.SKU, Reason: "adjustment is zero"})
			continue
		}
		if line.DestinationWarehouseID != nil {
			qty := line.Adjustment
			if qty < 0 {
				qty = -qty
			}
			if _, err := s.AdjustStock(ctx, tenantID, AdjustStockRequest{
				SKU:         line.SKU,
				Adjustment:  -qty,
				Reason:      "transfer_out",
				Reference:   req.Reference,
				Notes:       req.Notes,
				AdjustedBy:  req.AdjustedBy,
				WarehouseID: req.WarehouseID,
				OutletID:    req.OutletID,
			}); err != nil {
				res.Skipped = append(res.Skipped, BulkAdjustSkipped{SKU: line.SKU, Reason: err.Error()})
				continue
			}
			if _, err := s.AdjustStock(ctx, tenantID, AdjustStockRequest{
				SKU:         line.SKU,
				Adjustment:  qty,
				Reason:      "transfer_in",
				Reference:   req.Reference,
				Notes:       req.Notes,
				AdjustedBy:  req.AdjustedBy,
				WarehouseID: *line.DestinationWarehouseID,
				OutletID:    req.OutletID,
			}); err != nil {
				// The source leg already moved stock out — flag this distinctly rather than as a
				// plain skip, since the item is now unaccounted for at neither warehouse and needs
				// a human to reconcile it, not just retry the whole line.
				res.Skipped = append(res.Skipped, BulkAdjustSkipped{SKU: line.SKU, Reason: "moved out of source but destination leg failed: " + err.Error()})
				continue
			}
			if itemID, ok := itemIDBySKU[line.SKU]; ok {
				dest := *line.DestinationWarehouseID
				movedByDest[dest] = append(movedByDest[dest], transfers.CompletedTransferLine{ItemID: itemID, Quantity: qty})
			}
			res.Processed++
			continue
		}
		_, err := s.AdjustStock(ctx, tenantID, AdjustStockRequest{
			SKU:         line.SKU,
			Adjustment:  line.Adjustment,
			Reason:      req.Reason,
			Reference:   req.Reference,
			Notes:       req.Notes,
			AdjustedBy:  req.AdjustedBy,
			WarehouseID: req.WarehouseID,
			OutletID:    req.OutletID,
		})
		if err != nil {
			res.Skipped = append(res.Skipped, BulkAdjustSkipped{SKU: line.SKU, Reason: err.Error()})
			continue
		}
		res.Processed++
	}

	if s.transferRecorder != nil {
		for destWarehouseID, lines := range movedByDest {
			if _, rErr := s.transferRecorder.RecordCompletedTransfer(
				ctx, tenantID, req.WarehouseID, destWarehouseID, "bulk_adjust", lines, req.AdjustedBy, req.Notes,
			); rErr != nil {
				// The stock has already moved successfully — a failure here only means this batch
				// won't show up in the Transfers list with its own transfer_number, not a lost or
				// reversed move. Log and move on rather than surface it as a bulk-adjust failure.
				s.log.Warn("bulk adjust: record completed transfer audit failed",
					zap.Error(rErr), zap.String("destination_warehouse_id", destWarehouseID.String()))
			}
		}
	}

	return res, nil
}
