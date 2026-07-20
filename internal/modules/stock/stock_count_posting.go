package stock

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	entcountline "github.com/bengobox/inventory-service/internal/ent/stockcountline"
)

// varianceReasons are the StockAdjustment reasons a count line's variance may be
// classified as when posted. count_variance is the default for unclassified lines.
var varianceReasons = map[string]bool{
	"damaged": true, "expired": true, "shrinkage": true, "found": true,
	"correction": true, "count_variance": true, "other": true,
}

// PostCountResult summarizes a variance-posting pass over a stock count.
type PostCountResult struct {
	Posted        int      // lines whose variance posted this pass
	TotalVariance float64  // net signed quantity posted
	FailedSKUs    []string // lines whose adjustment failed (left unposted for retry)
	Finalized     bool     // whether the count itself was marked approved (no failures)
}

// PostStockCountVariances posts the variance of every unposted, non-zero count line
// as a StockAdjustment (reusing AdjustStock so balances, ledger events and low-stock
// checks all fire) and marks each posted line Posted(true). When every line posts it
// finalizes the count — status=approved with the approver stamped.
//
// It is the single source of truth for turning a reviewed count into stock movement,
// shared by the stock-count Approve endpoint and the approval-execution path so a
// count posts identically whether the operator approves it directly or a manager
// signs it off through the approval matrix. The Posted flag makes it idempotent: a
// second pass skips already-posted lines, so double-approving never double-counts.
func (s *Service) PostStockCountVariances(ctx context.Context, tenantID, countID, approver uuid.UUID) (PostCountResult, error) {
	var res PostCountResult

	count, err := s.client.StockCount.Query().
		Where(entcount.ID(countID), entcount.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return res, err
	}

	lines, err := s.client.StockCountLine.Query().
		Where(entcountline.StockCountID(countID), entcountline.Posted(false)).
		All(ctx)
	if err != nil {
		return res, err
	}

	for _, ln := range lines {
		if ln.CountedQty == nil || ln.Variance == nil || *ln.Variance == 0 {
			continue
		}
		// The reviewer's classification (wastage/damaged, pilferage/shrinkage, found, …)
		// becomes the adjustment reason; unclassified lines post as count_variance. The
		// note records the direction so reports read "shortage"/"surplus" without decoding
		// signs.
		reason := ln.Reason
		if reason == "" || !varianceReasons[reason] {
			reason = "count_variance"
		}
		direction := "surplus found"
		if *ln.Variance < 0 {
			direction = "shortage"
		}
		if _, adjErr := s.AdjustStock(ctx, tenantID, AdjustStockRequest{
			SKU:         ln.Sku,
			Adjustment:  *ln.Variance,
			Reason:      reason,
			Reference:   "stock-count:" + countID.String(),
			Notes:       "stock take " + direction + " — physical count reconciliation",
			AdjustedBy:  approver,
			WarehouseID: count.WarehouseID,
		}); adjErr != nil {
			s.log.Warn("post count variance failed", zap.String("sku", ln.Sku), zap.Error(adjErr))
			res.FailedSKUs = append(res.FailedSKUs, ln.Sku)
			continue
		}
		_, _ = ln.Update().SetPosted(true).Save(ctx)
		res.Posted++
		res.TotalVariance += *ln.Variance
	}

	// All-or-nothing finalize: only mark the count approved once EVERY variance line has
	// posted. Failed lines stay Posted(false) so a later pass retries just those, instead
	// of the count being approved with some variances silently dropped.
	if len(res.FailedSKUs) == 0 && count.Status != "approved" {
		if _, err := count.Update().
			SetStatus("approved").
			SetApprovedBy(approver).
			SetApprovedAt(time.Now()).
			Save(ctx); err != nil {
			return res, err
		}
		res.Finalized = true
	}
	return res, nil
}
