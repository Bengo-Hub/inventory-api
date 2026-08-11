package stock

import (
	"context"
	"fmt"
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
type SetItemOutletMembershipRequest struct {
	ItemIDs            []uuid.UUID
	TargetWarehouseIDs []uuid.UUID
	AdjustedBy         uuid.UUID
	Notes              string
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
}

// SetItemOutletMembership reconciles each item's current warehouse footprint (every warehouse
// where it has an ACTIVE, non-removed balance) against req.TargetWarehouseIDs:
//   - warehouses dropped from the target are zeroed and marked removed_from_location=true, exactly
//     like RelocateItemLocation's source side;
//   - warehouses newly added receive an active balance, created if none exists there yet;
//   - the total on_hand pulled from dropped warehouses is split evenly across newly-added ones
//     (remainder to the last) — the natural generalization of RelocateItemLocation's single
//     source→destination carry-over to an arbitrary many-to-many toggle. Dropping outlets with
//     nothing newly added simply withdraws that quantity (an explicit "stock nowhere" removal);
//     adding outlets with nothing dropped starts them at zero (nothing to carry over, matching a
//     brand new location assignment).
//   - warehouses that are already in their target state (present-and-kept, or
//     absent-and-still-unwanted) are left untouched entirely.
//
// Idempotent: an item already matching its target set is skipped, never an error.
func (s *Service) SetItemOutletMembership(ctx context.Context, tenantID uuid.UUID, req SetItemOutletMembershipRequest) (*SetItemOutletMembershipResult, error) {
	res := &SetItemOutletMembershipResult{Skipped: []MembershipSkipped{}}
	if len(req.ItemIDs) == 0 {
		return res, nil
	}

	targetSet := make(map[uuid.UUID]struct{}, len(req.TargetWarehouseIDs))
	for _, id := range req.TargetWarehouseIDs {
		if id == uuid.Nil {
			continue
		}
		if _, err := s.client.Warehouse.Query().
			Where(warehouse.ID(id), warehouse.TenantID(tenantID)).
			Only(ctx); err != nil {
			return nil, fmt.Errorf("stock: target warehouse %s not found: %w", id, err)
		}
		targetSet[id] = struct{}{}
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

		var pooledOnHand, pooledAvailable float64
		var template *ent.InventoryBalance
		for _, bal := range toRemove {
			pooledOnHand += bal.OnHand
			pooledAvailable += bal.Available
			if template == nil {
				template = bal
			}
			if _, uErr := tx.InventoryBalance.UpdateOne(bal).
				SetOnHand(0).
				SetAvailable(0).
				SetRemovedFromLocation(true).
				Save(ctx); uErr != nil {
				err = fmt.Errorf("stock: remove item %s from warehouse %s: %w", itemID, bal.WarehouseID, uErr)
				return nil, err
			}
			s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, bal.WarehouseID, bal.OnHand, 0, req, now)
			s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, bal.WarehouseID, bal.Available, 0, "location_move")
		}

		if len(toAdd) > 0 {
			share := pooledOnHand / float64(len(toAdd))
			availShare := pooledAvailable / float64(len(toAdd))
			for i, whID := range toAdd {
				onHandHere, availHere := share, availShare
				if i == len(toAdd)-1 {
					// Remainder correction so the sum of shares exactly equals the pooled total
					// (float division across N recipients can otherwise lose a fractional unit).
					onHandHere = pooledOnHand - share*float64(len(toAdd)-1)
					availHere = pooledAvailable - availShare*float64(len(toAdd)-1)
				}
				existing, gErr := tx.InventoryBalance.Query().
					Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itemID), inventorybalance.WarehouseID(whID)).
					Only(ctx)
				switch {
				case ent.IsNotFound(gErr):
					create := tx.InventoryBalance.Create().
						SetTenantID(tenantID).
						SetItemID(itemID).
						SetWarehouseID(whID).
						SetOnHand(onHandHere).
						SetAvailable(availHere)
					if template != nil {
						create = create.
							SetUnitOfMeasure(template.UnitOfMeasure).
							SetReorderLevel(template.ReorderLevel).
							SetReorderQuantity(template.ReorderQuantity).
							SetNillablePreferredSupplierID(template.PreferredSupplierID).
							SetAutoReorderEnabled(template.AutoReorderEnabled)
					}
					if _, cErr := create.Save(ctx); cErr != nil {
						err = fmt.Errorf("stock: add item %s to warehouse %s: %w", itemID, whID, cErr)
						return nil, err
					}
					s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, whID, 0, onHandHere, req, now)
					s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, whID, 0, availHere, "location_move")
				case gErr != nil:
					err = fmt.Errorf("stock: query existing balance for item %s at warehouse %s: %w", itemID, whID, gErr)
					return nil, err
				default:
					// A removed balance already exists here (the item was here before, then
					// removed, and is being re-added) — reactivate it and add the pooled share.
					before := existing.OnHand
					beforeAvail := existing.Available
					if _, uErr := tx.InventoryBalance.UpdateOne(existing).
						SetOnHand(before + onHandHere).
						SetAvailable(beforeAvail + availHere).
						SetRemovedFromLocation(false).
						Save(ctx); uErr != nil {
						err = fmt.Errorf("stock: reactivate item %s at warehouse %s: %w", itemID, whID, uErr)
						return nil, err
					}
					s.recordMembershipAdjustment(ctx, tx, tenantID, itemID, whID, before, before+onHandHere, req, now)
					s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, whID, beforeAvail, beforeAvail+availHere, "location_move")
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

// recordMembershipAdjustment writes the audit trail row for one warehouse's side of a membership
// change — factored out since both the "remove" and "add" branches need an identical shape.
func (s *Service) recordMembershipAdjustment(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, before, after float64, req SetItemOutletMembershipRequest, now time.Time) {
	adj := tx.StockAdjustment.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetWarehouseID(warehouseID).
		SetQuantityBefore(before).
		SetQuantityChange(after - before).
		SetQuantityAfter(after).
		SetReason(stockadjustment.ReasonLocationMove).
		SetAdjustedAt(now)
	if req.AdjustedBy != uuid.Nil {
		adj = adj.SetAdjustedBy(req.AdjustedBy)
	}
	if req.Notes != "" {
		adj = adj.SetNotes(req.Notes)
	}
	if _, err := adj.Save(ctx); err != nil {
		s.log.Warn("membership adjustment audit row failed", zap.Error(err), zap.String("item_id", itemID.String()))
	}
}
