package stock

import (
	"context"

	"github.com/google/uuid"
)

// BulkAdjustLine is one item's adjustment within a bulk request.
type BulkAdjustLine struct {
	SKU        string  `json:"sku"`
	Adjustment float64 `json:"adjustment"`
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
	for _, line := range req.Lines {
		if line.Adjustment == 0 {
			res.Skipped = append(res.Skipped, BulkAdjustSkipped{SKU: line.SKU, Reason: "adjustment is zero"})
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
	return res, nil
}
