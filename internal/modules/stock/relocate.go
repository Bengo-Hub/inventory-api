package stock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// RelocateItemLocationRequest moves one or many items' ENTIRE current balance — whatever it is
// right now, including zero — from one warehouse to another. This is deliberately NOT a stock
// transfer: a transfer moves a CHOSEN quantity between two balances that both continue to exist,
// gated on the source having enough available to ship. A relocation has no chosen quantity —
// there's nothing to be "insufficient" for — it just carries the item's whole presence at the
// source over to the destination and marks the source removed_from_location, so it stops
// appearing there (see items.ListItems' outlet-scoping and extras_stock.go's queryStockLevels).
type RelocateItemLocationRequest struct {
	ItemIDs                []uuid.UUID
	SourceWarehouseID      uuid.UUID
	DestinationWarehouseID uuid.UUID
	AdjustedBy             uuid.UUID
	Notes                  string
}

// RelocateSkipped explains why one item's location wasn't moved.
type RelocateSkipped struct {
	ItemID uuid.UUID `json:"item_id"`
	Reason string    `json:"reason"`
}

// RelocateItemLocationResult reports per-item outcomes (same shape as items.BulkActionResult).
type RelocateItemLocationResult struct {
	Processed int               `json:"processed"`
	Skipped   []RelocateSkipped `json:"skipped"`
}

// RelocateItemLocation moves each item's balance from the source warehouse to the destination
// warehouse wholesale — on_hand, available, and the balance's metadata (unit of measure, reorder
// settings, preferred supplier) carry over. If the destination already has a balance for that
// item (a multi-warehouse outlet, or the item was already partly there), the source's quantities
// are ADDED to it rather than overwriting existing destination stock. The source balance is
// zeroed and marked removed_from_location = true — it never sits at "0 in stock, still tracked
// here" the way a real stock-out does. Idempotent: an item with no balance at the source is
// skipped, never an error, matching the items/bulk.go precedent. `reserved` is deliberately left
// untouched on both sides (a pending reservation is tied to whatever system created it, e.g. an
// in-flight order); this mirrors transfers.adjustBalance, which never touches it either.
func (s *Service) RelocateItemLocation(ctx context.Context, tenantID uuid.UUID, req RelocateItemLocationRequest) (*RelocateItemLocationResult, error) {
	if req.SourceWarehouseID == uuid.Nil || req.DestinationWarehouseID == uuid.Nil {
		return nil, fmt.Errorf("stock: source and destination warehouse are required")
	}
	if req.SourceWarehouseID == req.DestinationWarehouseID {
		return nil, fmt.Errorf("stock: source and destination warehouse must be different")
	}
	res := &RelocateItemLocationResult{Skipped: []RelocateSkipped{}}
	if len(req.ItemIDs) == 0 {
		return res, nil
	}

	if _, err := s.client.Warehouse.Query().
		Where(warehouse.ID(req.SourceWarehouseID), warehouse.TenantID(tenantID)).
		Only(ctx); err != nil {
		return nil, fmt.Errorf("stock: source warehouse not found: %w", err)
	}
	if _, err := s.client.Warehouse.Query().
		Where(warehouse.ID(req.DestinationWarehouseID), warehouse.TenantID(tenantID)).
		Only(ctx); err != nil {
		return nil, fmt.Errorf("stock: destination warehouse not found: %w", err)
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
		srcBal, qErr := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itemID),
				inventorybalance.WarehouseID(req.SourceWarehouseID),
			).
			Only(ctx)
		if ent.IsNotFound(qErr) {
			res.Skipped = append(res.Skipped, RelocateSkipped{ItemID: itemID, Reason: "not present at source"})
			continue
		}
		if qErr != nil {
			err = fmt.Errorf("stock: query source balance for item %s: %w", itemID, qErr)
			return nil, err
		}

		srcOnHandBefore, srcAvailableBefore := srcBal.OnHand, srcBal.Available

		destBal, dErr := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itemID),
				inventorybalance.WarehouseID(req.DestinationWarehouseID),
			).
			Only(ctx)
		var destOnHandBefore, destAvailableBefore float64
		switch {
		case ent.IsNotFound(dErr):
			if _, cErr := tx.InventoryBalance.Create().
				SetTenantID(tenantID).
				SetItemID(itemID).
				SetWarehouseID(req.DestinationWarehouseID).
				SetOnHand(srcOnHandBefore).
				SetAvailable(srcAvailableBefore).
				SetUnitOfMeasure(srcBal.UnitOfMeasure).
				SetReorderLevel(srcBal.ReorderLevel).
				SetReorderQuantity(srcBal.ReorderQuantity).
				SetNillablePreferredSupplierID(srcBal.PreferredSupplierID).
				SetAutoReorderEnabled(srcBal.AutoReorderEnabled).
				Save(ctx); cErr != nil {
				err = fmt.Errorf("stock: create destination balance for item %s: %w", itemID, cErr)
				return nil, err
			}
		case dErr != nil:
			err = fmt.Errorf("stock: query destination balance for item %s: %w", itemID, dErr)
			return nil, err
		default:
			destOnHandBefore, destAvailableBefore = destBal.OnHand, destBal.Available
			if _, uErr := tx.InventoryBalance.UpdateOne(destBal).
				SetOnHand(destOnHandBefore + srcOnHandBefore).
				SetAvailable(destAvailableBefore + srcAvailableBefore).
				SetRemovedFromLocation(false).
				Save(ctx); uErr != nil {
				err = fmt.Errorf("stock: update destination balance for item %s: %w", itemID, uErr)
				return nil, err
			}
		}
		destOnHandAfter := destOnHandBefore + srcOnHandBefore
		destAvailableAfter := destAvailableBefore + srcAvailableBefore

		if _, uErr := tx.InventoryBalance.UpdateOne(srcBal).
			SetOnHand(0).
			SetAvailable(0).
			SetRemovedFromLocation(true).
			Save(ctx); uErr != nil {
			err = fmt.Errorf("stock: zero source balance for item %s: %w", itemID, uErr)
			return nil, err
		}

		// Audit trail — one row per side, reusing the same ledger the item's stock history
		// panel already reads (stock/history.go), NOT the StockTransfer document type: this is
		// a single atomic relocation, not a draft/ship/receive/approval-gated document.
		srcAdj := tx.StockAdjustment.Create().
			SetTenantID(tenantID).
			SetItemID(itemID).
			SetWarehouseID(req.SourceWarehouseID).
			SetQuantityBefore(srcOnHandBefore).
			SetQuantityChange(-srcOnHandBefore).
			SetQuantityAfter(0).
			SetReason(stockadjustment.ReasonLocationMove).
			SetAdjustedAt(now)
		if req.AdjustedBy != uuid.Nil {
			srcAdj = srcAdj.SetAdjustedBy(req.AdjustedBy)
		}
		if req.Notes != "" {
			srcAdj = srcAdj.SetNotes(req.Notes)
		}
		if _, aErr := srcAdj.Save(ctx); aErr != nil {
			err = fmt.Errorf("stock: create source adjustment record for item %s: %w", itemID, aErr)
			return nil, err
		}

		destAdj := tx.StockAdjustment.Create().
			SetTenantID(tenantID).
			SetItemID(itemID).
			SetWarehouseID(req.DestinationWarehouseID).
			SetQuantityBefore(destOnHandBefore).
			SetQuantityChange(srcOnHandBefore).
			SetQuantityAfter(destOnHandAfter).
			SetReason(stockadjustment.ReasonLocationMove).
			SetAdjustedAt(now)
		if req.AdjustedBy != uuid.Nil {
			destAdj = destAdj.SetAdjustedBy(req.AdjustedBy)
		}
		if req.Notes != "" {
			destAdj = destAdj.SetNotes(req.Notes)
		}
		if _, aErr := destAdj.Save(ctx); aErr != nil {
			err = fmt.Errorf("stock: create destination adjustment record for item %s: %w", itemID, aErr)
			return nil, err
		}

		s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, req.SourceWarehouseID, srcAvailableBefore, 0, "location_move")
		s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, req.DestinationWarehouseID, destAvailableBefore, destAvailableAfter, "location_move")

		res.Processed++
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit relocation: %w", err)
	}
	return res, nil
}
