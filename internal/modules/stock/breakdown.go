package stock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
)

// BreakdownRequest converts bulk parent units into retail child units.
type BreakdownRequest struct {
	ParentSKU        string    `json:"parent_sku"`
	ChildSKU         string    `json:"child_sku"`
	WarehouseID      uuid.UUID `json:"warehouse_id,omitempty"`
	ParentQuantity   float64   `json:"parent_quantity"`
	ConversionFactor float64   `json:"conversion_factor"`
	ChildQuantity    float64   `json:"child_quantity,omitempty"` // derived from conversion_factor when omitted
	CostAllocated    *float64  `json:"cost_allocated,omitempty"`
	Reference        string    `json:"reference,omitempty"`
	Notes            string    `json:"notes,omitempty"`
	CreatedBy        uuid.UUID `json:"created_by,omitempty"`
}

// BreakdownResponse is the result of a stock breakdown.
type BreakdownResponse struct {
	ID             uuid.UUID `json:"id"`
	ParentSKU      string    `json:"parent_sku"`
	ChildSKU       string    `json:"child_sku"`
	ParentQuantity float64   `json:"parent_quantity"`
	ChildQuantity  float64   `json:"child_quantity"`
	ParentOnHand   float64   `json:"parent_on_hand"`
	ChildOnHand    float64   `json:"child_on_hand"`
	CreatedAt      time.Time `json:"created_at"`
}

// Breakdown atomically converts bulk parent units into retail child units: it decrements the
// parent SKU balance, increments the child SKU balance, writes both audit adjustments and a
// StockBreakdown header, and publishes inventory.stock.broken_down. Value is conserved by
// carrying cost_allocated parent->child. This is NOT BOM production.
func (s *Service) Breakdown(ctx context.Context, tenantID uuid.UUID, req BreakdownRequest) (*BreakdownResponse, error) {
	if req.ParentSKU == "" || req.ChildSKU == "" {
		return nil, fmt.Errorf("stock: parent_sku and child_sku are required")
	}
	if req.ParentSKU == req.ChildSKU {
		return nil, fmt.Errorf("stock: parent and child SKU must differ")
	}
	if req.ParentQuantity <= 0 {
		return nil, fmt.Errorf("stock: parent_quantity must be positive")
	}
	childQty := req.ChildQuantity
	if childQty <= 0 {
		if req.ConversionFactor <= 0 {
			return nil, fmt.Errorf("stock: provide child_quantity or a positive conversion_factor")
		}
		childQty = req.ParentQuantity * req.ConversionFactor
	}
	conv := req.ConversionFactor
	if conv <= 0 {
		conv = childQty / req.ParentQuantity
	}

	whID, err := s.resolveWarehouseID(ctx, tenantID, req.WarehouseID)
	if err != nil {
		return nil, err
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

	parentItem, err := tx.Item.Query().Where(item.TenantID(tenantID), item.Sku(req.ParentSKU)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: parent item not found: sku=%s: %w", req.ParentSKU, err)
	}
	childItem, err := tx.Item.Query().Where(item.TenantID(tenantID), item.Sku(req.ChildSKU)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: child item not found: sku=%s: %w", req.ChildSKU, err)
	}

	parentBal, err := tx.InventoryBalance.Query().Where(
		inventorybalance.TenantID(tenantID),
		inventorybalance.ItemID(parentItem.ID),
		inventorybalance.WarehouseID(whID),
	).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: no balance for parent sku=%s: %w", req.ParentSKU, err)
	}
	if parentBal.OnHand < req.ParentQuantity {
		err = fmt.Errorf("stock: insufficient parent stock: have %.4f, need %.4f", parentBal.OnHand, req.ParentQuantity)
		return nil, err
	}

	childBal, err := tx.InventoryBalance.Query().Where(
		inventorybalance.TenantID(tenantID),
		inventorybalance.ItemID(childItem.ID),
		inventorybalance.WarehouseID(whID),
	).First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("stock: query child balance: %w", err)
		}
		childBal, err = tx.InventoryBalance.Create().
			SetTenantID(tenantID).SetItemID(childItem.ID).SetWarehouseID(whID).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("stock: init child balance: %w", err)
		}
	}

	parentBefore := parentBal.OnHand
	parentAfter := parentBal.OnHand - req.ParentQuantity
	parentAvail := parentBal.Available - req.ParentQuantity
	if parentAvail < 0 {
		parentAvail = 0
	}
	if _, err = tx.InventoryBalance.UpdateOne(parentBal).SetOnHand(parentAfter).SetAvailable(parentAvail).Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: update parent balance: %w", err)
	}

	childBefore := childBal.OnHand
	childAfter := childBal.OnHand + childQty
	childAvail := childBal.Available + childQty
	if _, err = tx.InventoryBalance.UpdateOne(childBal).SetOnHand(childAfter).SetAvailable(childAvail).Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: update child balance: %w", err)
	}

	bdID := uuid.New()
	ref := req.Reference
	if ref == "" {
		ref = bdID.String()
	}
	now := time.Now()

	if _, err = tx.StockAdjustment.Create().
		SetTenantID(tenantID).SetItemID(parentItem.ID).SetWarehouseID(whID).
		SetQuantityBefore(parentBefore).SetQuantityChange(-req.ParentQuantity).SetQuantityAfter(parentAfter).
		SetReason(stockadjustment.ReasonOther).SetReference(ref).SetNotes("breakdown: parent consumed").
		SetAdjustedBy(req.CreatedBy).SetAdjustedAt(now).Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: parent adjustment: %w", err)
	}
	if _, err = tx.StockAdjustment.Create().
		SetTenantID(tenantID).SetItemID(childItem.ID).SetWarehouseID(whID).
		SetQuantityBefore(childBefore).SetQuantityChange(childQty).SetQuantityAfter(childAfter).
		SetReason(stockadjustment.ReasonOther).SetReference(ref).SetNotes("breakdown: child produced").
		SetAdjustedBy(req.CreatedBy).SetAdjustedAt(now).Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: child adjustment: %w", err)
	}

	bdBuilder := tx.StockBreakdown.Create().
		SetID(bdID).SetTenantID(tenantID).
		SetParentItemID(parentItem.ID).SetParentSku(req.ParentSKU).
		SetChildItemID(childItem.ID).SetChildSku(req.ChildSKU).
		SetWarehouseID(whID).
		SetParentQuantity(req.ParentQuantity).SetChildQuantity(childQty).SetConversionFactor(conv)
	if req.CostAllocated != nil {
		bdBuilder.SetCostAllocated(*req.CostAllocated)
	}
	if req.Reference != "" {
		bdBuilder.SetReference(req.Reference)
	}
	if req.Notes != "" {
		bdBuilder.SetNotes(req.Notes)
	}
	if req.CreatedBy != uuid.Nil {
		bdBuilder.SetCreatedBy(req.CreatedBy)
	}
	if _, err = bdBuilder.Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: create breakdown: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, parentItem.ID, "inventory", "stock.broken_down", map[string]any{
		"tenant_id":         tenantID.String(),
		"breakdown_id":      bdID.String(),
		"parent_sku":        req.ParentSKU,
		"child_sku":         req.ChildSKU,
		"warehouse_id":      whID.String(),
		"parent_quantity":   req.ParentQuantity,
		"child_quantity":    childQty,
		"conversion_factor": conv,
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit breakdown: %w", err)
	}

	return &BreakdownResponse{
		ID: bdID, ParentSKU: req.ParentSKU, ChildSKU: req.ChildSKU,
		ParentQuantity: req.ParentQuantity, ChildQuantity: childQty,
		ParentOnHand: parentAfter, ChildOnHand: childAfter, CreatedAt: now,
	}, nil
}
