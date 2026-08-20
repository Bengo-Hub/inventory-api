package stock

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// SetItemOutletMembershipRequest declares, per item, the COMPLETE set of warehouses it should be
// present in — the checkbox UX: check an outlet to stock the item there, uncheck to remove it,
// independent of how many outlets are being toggled at once. Distinct from
// RelocateItemLocationRequest's single source→destination pair, which this supersedes for the
// catalog UI (kept as the lower-level primitive RelocateItemLocation still uses internally).
//
// What happens to a warehouse DROPPED from the target set is governed by three mutually
// exclusive modes (see ValidateSetItemOutletMembershipRequest for the exclusivity rule):
//   - default (MoveWithStock=false, ZeroStockMode=false): HIDE ONLY. on_hand/available are left
//     exactly as they were — only removed_from_location flips to true, so the item stops
//     appearing there. Re-checking that same outlet later restores it with the identical
//     quantity it had when hidden. This is the safe default: unchecking an outlet must never
//     move or clear real stock unless the caller explicitly asks for that below.
//   - MoveWithStock=true: carries the dropped warehouses' quantity to the newly-checked
//     warehouse(s). Requires TargetWarehouseIDs to be non-empty (validated below — "where would
//     the stock go?"). An item whose OWN toAdd set ends up empty even though the request overall
//     has targets (e.g. it already sits at every target warehouse while dropping some other one)
//     falls back to a hide for that item — never a silent discard — and is reported via
//     SetItemOutletMembershipResult.FallbackApplied.
//   - ZeroStockMode=true: the dropped warehouses' stock is discarded outright (not carried
//     anywhere) and newly-checked warehouses start at zero — an explicit, deliberate write-off
//     the UI gates behind a confirmation warning.
type SetItemOutletMembershipRequest struct {
	ItemIDs            []uuid.UUID
	TargetWarehouseIDs []uuid.UUID
	AdjustedBy         uuid.UUID
	Notes              string
	// ZeroStockMode: dropped warehouses' stock is discarded rather than hidden/moved, and
	// newly-added warehouses start at zero. Mutually exclusive with MoveWithStock.
	ZeroStockMode bool
	// MoveWithStock: dropped warehouses' quantity is carried to the newly-added warehouse(s)
	// instead of just being hidden. Requires at least one warehouse in TargetWarehouseIDs — see
	// ValidateSetItemOutletMembershipRequest. Mutually exclusive with ZeroStockMode.
	MoveWithStock bool
	// MoveQuantity, only meaningful when MoveWithStock is set: for a clean 1-dropped+1-added
	// pair, moves exactly this amount instead of the item's whole balance, leaving the remainder
	// active (hidden, never discarded) at the source. Ignored for any other shape (N:M, or a
	// pair this item doesn't happen to have) — those fall back to the pooled-split move below.
	MoveQuantity *float64
}

// MembershipSkipped explains why one item's membership wasn't changed.
type MembershipSkipped struct {
	ItemID uuid.UUID `json:"item_id"`
	Reason string    `json:"reason"`
}

// SetItemOutletMembershipResult reports per-item outcomes (same shape as items.BulkActionResult).
type SetItemOutletMembershipResult struct {
	Processed int                 `json:"processed"`
	Skipped   []MembershipSkipped `json:"skipped"`
	// FallbackApplied lists items that asked for MoveWithStock but had no destination of their
	// own (already present at every target warehouse while dropping some other one) — their
	// drop was hidden instead of discarded, never silently. Always empty unless MoveWithStock
	// was set.
	FallbackApplied []uuid.UUID `json:"fallback_applied,omitempty"`
}

// ValidateSetItemOutletMembershipRequest checks the request shape before any DB work — pure and
// unit-testable without ent. Called from the HTTP handler (so a bad request gets an immediate
// 400 instead of a background job that reports "failed" later) and defensively again at the top
// of SetItemOutletMembership for any other caller.
func ValidateSetItemOutletMembershipRequest(req SetItemOutletMembershipRequest) error {
	if req.MoveWithStock && req.ZeroStockMode {
		return fmt.Errorf("stock: choose either move-with-stock or move-with-zero-stock, not both")
	}
	if req.MoveWithStock && len(req.TargetWarehouseIDs) == 0 {
		return fmt.Errorf("stock: select at least one destination outlet to move stock into")
	}
	if req.MoveQuantity != nil {
		if !req.MoveWithStock {
			return fmt.Errorf("stock: move_quantity requires move_with_stock")
		}
		if *req.MoveQuantity <= 0 {
			return fmt.Errorf("stock: move_quantity must be greater than zero")
		}
	}
	return nil
}

// membershipMode is the resolved, mutually-exclusive mode for one request — derived once so the
// per-item loop never has to re-check the two raw booleans.
type membershipMode int

const (
	modeHide membershipMode = iota // default: preserve quantity, just hide
	modeMoveWithStock
	modeZeroStock
)

func membershipModeOf(req SetItemOutletMembershipRequest) membershipMode {
	switch {
	case req.MoveWithStock:
		return modeMoveWithStock
	case req.ZeroStockMode:
		return modeZeroStock
	default:
		return modeHide
	}
}

// reactivateStrategy decides how a toAdd warehouse's existing (possibly previously-hidden)
// balance combines with whatever is "incoming" (0 for a plain hide/add, a pooled/moved share for
// move-with-stock, forced-to-zero for zero-stock).
type reactivateStrategy int

const (
	reactivatePreserve reactivateStrategy = iota // before + 0 — hide default, unchanged on unhide
	reactivateAdd                                // before + incoming — move-with-stock
	reactivateReset                               // incoming exactly, discards before — zero-stock
)

// membershipPlan is what planMembershipChange decides for ONE item's toRemove/toAdd sets — pure
// (no ent, no I/O), so the mode-branching table is unit-testable without a database.
// SetItemOutletMembership computes toRemoveCount/toAddCount/pooled sums from real balances and
// applies whatever this plan says.
type membershipPlan struct {
	// SourceZeroed: true discards the toRemove balances (zero-stock mode); false hides them with
	// their quantity untouched (default hide, and the move-with-stock per-item fallback).
	SourceZeroed bool
	// PooledOnHand/PooledAvailable: total to redistribute across toAdd — 0 unless this item is
	// genuinely moving stock to a real destination.
	PooledOnHand, PooledAvailable float64
	TargetStrategy                reactivateStrategy
	// Fallback: MoveWithStock was requested but this item's own toAdd is empty — its drop was
	// hidden instead of moved/discarded, and the caller must report this, never lose data.
	Fallback bool
}

// planMembershipChange decides, in pure/testable form, how one item's toRemove/toAdd sets
// should be treated under mode. pooledOnHand/pooledAvailable are the sums of the toRemove
// balances' on_hand/available as already queried by the caller (0 when toRemove is empty).
func planMembershipChange(mode membershipMode, toRemoveCount, toAddCount int, pooledOnHand, pooledAvailable float64) membershipPlan {
	switch mode {
	case modeZeroStock:
		return membershipPlan{SourceZeroed: true, TargetStrategy: reactivateReset}
	case modeMoveWithStock:
		if toRemoveCount > 0 && toAddCount == 0 {
			return membershipPlan{TargetStrategy: reactivatePreserve, Fallback: true}
		}
		return membershipPlan{
			PooledOnHand:    pooledOnHand,
			PooledAvailable: pooledAvailable,
			TargetStrategy:  reactivateAdd,
		}
	default: // modeHide
		return membershipPlan{TargetStrategy: reactivatePreserve}
	}
}

// SetItemOutletMembership reconciles each item's current warehouse footprint (every warehouse
// where it has an ACTIVE, non-removed balance) against req.TargetWarehouseIDs — see the type doc
// on SetItemOutletMembershipRequest for the three modes this can operate in.
//
// Idempotent: an item already matching its target set is skipped, never an error.
func (s *Service) SetItemOutletMembership(ctx context.Context, tenantID uuid.UUID, req SetItemOutletMembershipRequest) (*SetItemOutletMembershipResult, error) {
	if vErr := ValidateSetItemOutletMembershipRequest(req); vErr != nil {
		return nil, vErr
	}

	res := &SetItemOutletMembershipResult{Skipped: []MembershipSkipped{}}
	if len(req.ItemIDs) == 0 {
		return res, nil
	}

	mode := membershipModeOf(req)

	targetSet := make(map[uuid.UUID]struct{}, len(req.TargetWarehouseIDs))
	whNames := make(map[uuid.UUID]string, len(req.TargetWarehouseIDs))
	for _, id := range req.TargetWarehouseIDs {
		if id == uuid.Nil {
			continue
		}
		wh, wErr := s.client.Warehouse.Query().
			Where(warehouse.ID(id), warehouse.TenantID(tenantID)).
			Only(ctx)
		if wErr != nil {
			return nil, fmt.Errorf("stock: target warehouse %s not found: %w", id, wErr)
		}
		targetSet[id] = struct{}{}
		whNames[id] = wh.Name
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now()

	for _, itemID := range req.ItemIDs {
		bals, qErr := tx.InventoryBalance.Query().
			Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itemID)).
			All(ctx)
		if qErr != nil {
			err = fmt.Errorf("stock: query balances for item %s: %w", itemID, qErr)
			return nil, err
		}

		currentActive := make(map[uuid.UUID]*ent.InventoryBalance, len(bals))
		for _, b := range bals {
			if !b.RemovedFromLocation {
				currentActive[b.WarehouseID] = b
			}
		}

		var toRemove []*ent.InventoryBalance
		for whID, bal := range currentActive {
			if _, keep := targetSet[whID]; !keep {
				toRemove = append(toRemove, bal)
			}
		}
		var toAdd []uuid.UUID
		for whID := range targetSet {
			if _, present := currentActive[whID]; !present {
				toAdd = append(toAdd, whID)
			}
		}

		if len(toRemove) == 0 && len(toAdd) == 0 {
			res.Skipped = append(res.Skipped, MembershipSkipped{ItemID: itemID, Reason: "already matches target outlets"})
			continue
		}

		// Backfill display names for any toRemove warehouse not already known from the target
		// set — needed so "Moved to/from" notes read as a real outlet name, not a bare UUID.
		for _, bal := range toRemove {
			if _, ok := whNames[bal.WarehouseID]; !ok {
				if wh, wErr := tx.Warehouse.Get(ctx, bal.WarehouseID); wErr == nil {
					whNames[bal.WarehouseID] = wh.Name
				}
			}
		}

		// Partial transfer: a clean 1-dropped+1-added pair with an explicit MoveQuantity less
		// than the source's on-hand, under move-with-stock only. Leaves the remainder active
		// (hidden, never discarded) at the source instead of carrying everything to the
		// destination — see the type doc on MoveQuantity.
		if mode == modeMoveWithStock && req.MoveQuantity != nil && *req.MoveQuantity > 0 && len(toRemove) == 1 && len(toAdd) == 1 {
			srcBal := toRemove[0]
			qty := *req.MoveQuantity
			if qty < srcBal.OnHand {
				remainOnHand := srcBal.OnHand - qty
				remainAvail := srcBal.Available - qty
				if remainAvail < 0 {
					remainAvail = 0
				}
				if _, uErr := tx.InventoryBalance.UpdateOne(srcBal).
					SetOnHand(remainOnHand).
					SetAvailable(remainAvail).
					SetRemovedFromLocation(true).
					Save(ctx); uErr != nil {
					err = fmt.Errorf("stock: partial-move source for item %s at warehouse %s: %w", itemID, srcBal.WarehouseID, uErr)
					return nil, err
				}
				s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, srcBal.WarehouseID, srcBal.OnHand, remainOnHand,
					stockadjustment.ReasonLocationMove, "Moved to "+joinWarehouseNames(toAdd, whNames), req, now)
				if remainAvail != srcBal.Available {
					s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, srcBal.WarehouseID, srcBal.Available, remainAvail, "location_move")
				}
				if _, aErr := s.addOrReactivateMembershipTarget(ctx, tx, tenantID, itemID, toAdd[0], qty, qty,
					reactivateAdd, srcBal, "Moved from "+joinWarehouseNames([]uuid.UUID{srcBal.WarehouseID}, whNames), req, now); aErr != nil {
					err = aErr
					return nil, err
				}
				res.Processed++
				continue
			}
			// qty >= source on-hand: identical to a full move, fall through to the normal path.
		}

		var pooledOnHand, pooledAvailable float64
		for _, bal := range toRemove {
			pooledOnHand += bal.OnHand
			pooledAvailable += bal.Available
		}
		plan := planMembershipChange(mode, len(toRemove), len(toAdd), pooledOnHand, pooledAvailable)
		if plan.Fallback {
			res.FallbackApplied = append(res.FallbackApplied, itemID)
		}

		var template *ent.InventoryBalance
		for _, bal := range toRemove {
			if template == nil {
				template = bal
			}
			newOnHand, newAvailable := bal.OnHand, bal.Available
			if plan.SourceZeroed || (mode == modeMoveWithStock && !plan.Fallback) {
				newOnHand, newAvailable = 0, 0
			}
			if _, uErr := tx.InventoryBalance.UpdateOne(bal).
				SetOnHand(newOnHand).
				SetAvailable(newAvailable).
				SetRemovedFromLocation(true).
				Save(ctx); uErr != nil {
				err = fmt.Errorf("stock: remove item %s from warehouse %s: %w", itemID, bal.WarehouseID, uErr)
				return nil, err
			}

			reason := stockadjustment.ReasonLocationHidden
			var notes string
			switch {
			case plan.SourceZeroed:
				reason = stockadjustment.ReasonLocationMove
				notes = "Discarded (moved with zero stock, no carry-over)"
			case plan.Fallback:
				notes = "Kept — no destination outlet was selected for the move"
			case mode == modeMoveWithStock:
				reason = stockadjustment.ReasonLocationMove
				notes = "Moved to " + joinWarehouseNames(toAdd, whNames)
			default:
				notes = fmt.Sprintf("Hidden from outlet — %s units retained", formatQty(bal.OnHand))
			}
			s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, bal.WarehouseID, bal.OnHand, newOnHand, reason, notes, req, now)
			if newAvailable != bal.Available {
				s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, bal.WarehouseID, bal.Available, newAvailable, "location_move")
			}
		}

		if len(toAdd) > 0 {
			switch plan.TargetStrategy {
			case reactivateAdd:
				share := plan.PooledOnHand / float64(len(toAdd))
				availShare := plan.PooledAvailable / float64(len(toAdd))
				moveNotes := ""
				if len(toRemove) > 0 {
					moveNotes = "Moved from " + joinWarehouseNames(warehouseIDsOf(toRemove), whNames)
				}
				for i, whID := range toAdd {
					onHandHere, availHere := share, availShare
					if i == len(toAdd)-1 {
						// Remainder correction so the sum of shares exactly equals the pooled
						// total (float division across N recipients can otherwise lose a
						// fractional unit).
						onHandHere = plan.PooledOnHand - share*float64(len(toAdd)-1)
						availHere = plan.PooledAvailable - availShare*float64(len(toAdd)-1)
					}
					if _, aErr := s.addOrReactivateMembershipTarget(ctx, tx, tenantID, itemID, whID, onHandHere, availHere, plan.TargetStrategy, template, moveNotes, req, now); aErr != nil {
						err = aErr
						return nil, err
					}
				}
			default: // reactivatePreserve (hide-default add/unhide) or reactivateReset (zero-stock add)
				for _, whID := range toAdd {
					if _, aErr := s.addOrReactivateMembershipTarget(ctx, tx, tenantID, itemID, whID, 0, 0, plan.TargetStrategy, template, "", req, now); aErr != nil {
						err = aErr
						return nil, err
					}
				}
			}
		}

		res.Processed++
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit membership change: %w", err)
	}
	return res, nil
}

// addOrReactivateMembershipTarget gives item itemID a balance at warehouseID: creates one
// (copying template's unit/reorder/supplier metadata if given) if none exists yet, or reactivates
// an existing removed-from-location balance. strategy decides how the existing (possibly hidden)
// quantity combines with incomingOnHand/incomingAvailable — see reactivateStrategy's doc comment.
// moveNotes is only used for reactivateAdd (a real move); the other two strategies generate their
// own explanatory notes internally, since they never depend on where the stock came from.
func (s *Service) addOrReactivateMembershipTarget(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, incomingOnHand, incomingAvailable float64, strategy reactivateStrategy, template *ent.InventoryBalance, moveNotes string, req SetItemOutletMembershipRequest, now time.Time) (*ent.InventoryBalance, error) {
	existing, gErr := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itemID), inventorybalance.WarehouseID(warehouseID)).
		Only(ctx)
	switch {
	case ent.IsNotFound(gErr):
		create := tx.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itemID).
			SetWarehouseID(warehouseID).
			SetOnHand(incomingOnHand).
			SetAvailable(incomingAvailable)
		if template != nil {
			create = create.
				SetUnitOfMeasure(template.UnitOfMeasure).
				SetReorderLevel(template.ReorderLevel).
				SetReorderQuantity(template.ReorderQuantity).
				SetNillablePreferredSupplierID(template.PreferredSupplierID).
				SetAutoReorderEnabled(template.AutoReorderEnabled)
		}
		created, cErr := create.Save(ctx)
		if cErr != nil {
			return nil, fmt.Errorf("stock: add item %s to warehouse %s: %w", itemID, warehouseID, cErr)
		}
		notes := moveNotes
		switch strategy {
		case reactivatePreserve:
			notes = "Added to outlet — starts at zero"
		case reactivateReset:
			notes = "Starts at zero (moved with zero stock)"
		}
		s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, warehouseID, 0, incomingOnHand, stockadjustment.ReasonLocationMove, notes, req, now)
		if incomingAvailable != 0 {
			s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, warehouseID, 0, incomingAvailable, "location_move")
		}
		return created, nil
	case gErr != nil:
		return nil, fmt.Errorf("stock: query existing balance for item %s at warehouse %s: %w", itemID, warehouseID, gErr)
	default:
		// A balance already exists here (the item was here before, then removed — hidden or
		// discarded — and is being re-added now).
		before, beforeAvail := existing.OnHand, existing.Available
		var afterOnHand, afterAvail float64
		var reason stockadjustment.Reason
		var notes string
		switch strategy {
		case reactivateAdd:
			afterOnHand, afterAvail = before+incomingOnHand, beforeAvail+incomingAvailable
			reason, notes = stockadjustment.ReasonLocationMove, moveNotes
		case reactivateReset:
			afterOnHand, afterAvail = incomingOnHand, incomingAvailable
			reason, notes = stockadjustment.ReasonLocationMove, "Reset to zero (moved with zero stock)"
		default: // reactivatePreserve — hide-mode unhide, always a true no-op on quantity
			afterOnHand, afterAvail = before, beforeAvail
			reason, notes = stockadjustment.ReasonLocationUnhidden, fmt.Sprintf("Unhidden — %s units restored", formatQty(before))
		}
		updated, uErr := tx.InventoryBalance.UpdateOne(existing).
			SetOnHand(afterOnHand).
			SetAvailable(afterAvail).
			SetRemovedFromLocation(false).
			Save(ctx)
		if uErr != nil {
			return nil, fmt.Errorf("stock: reactivate item %s at warehouse %s: %w", itemID, warehouseID, uErr)
		}
		s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, warehouseID, before, afterOnHand, reason, notes, req, now)
		if afterAvail != beforeAvail {
			s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, warehouseID, beforeAvail, afterAvail, "location_move")
		}
		return updated, nil
	}
}

// recordMembershipAdjustment writes the audit trail row for one warehouse's side of a membership
// change. autoNotes explains WHAT happened (e.g. "Moved to X", "Hidden from outlet — N units
// retained") and is combined with any free-text note the caller supplied, so neither is lost.
func (s *Service) recordMembershipAdjustment(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, before, after float64, reason stockadjustment.Reason, autoNotes string, req SetItemOutletMembershipRequest, now time.Time) {
	notes := autoNotes
	if req.Notes != "" {
		if notes != "" {
			notes = req.Notes + " — " + notes
		} else {
			notes = req.Notes
		}
	}
	adj := tx.StockAdjustment.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetWarehouseID(warehouseID).
		SetQuantityBefore(before).
		SetQuantityChange(after - before).
		SetQuantityAfter(after).
		SetReason(reason).
		SetAdjustedAt(now)
	if req.AdjustedBy != uuid.Nil {
		adj = adj.SetAdjustedBy(req.AdjustedBy)
	}
	if notes != "" {
		adj = adj.SetNotes(notes)
	}
	if _, err := adj.Save(ctx); err != nil {
		s.log.Warn("membership adjustment audit row failed", zap.Error(err), zap.String("item_id", itemID.String()))
	}
}

// warehouseIDsOf extracts warehouse IDs from a slice of balances — used to build "Moved from …"
// notes text without threading a second parallel ID slice through the toRemove loop.
func warehouseIDsOf(bals []*ent.InventoryBalance) []uuid.UUID {
	ids := make([]uuid.UUID, len(bals))
	for i, b := range bals {
		ids[i] = b.WarehouseID
	}
	return ids
}

// joinWarehouseNames renders a human-readable, comma-separated outlet name list for
// StockAdjustment notes — falls back to a generic phrase if a name can't be resolved (should
// only happen for a warehouse deleted between the lookup and this call).
func joinWarehouseNames(ids []uuid.UUID, names map[uuid.UUID]string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if n, ok := names[id]; ok && n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 0 {
		return "another outlet"
	}
	return strings.Join(parts, ", ")
}

// formatQty renders a quantity for human-readable notes text without float noise (e.g. "42"
// rather than "42.000000").
func formatQty(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
