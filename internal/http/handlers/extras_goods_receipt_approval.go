package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
)

// Goods receipts under the approval matrix.
//
// A goods receipt (GRN) is created as a draft and only moves stock when it is posted. When a
// goods_receipt approval rule matches, posting waits for sign-off. The rules this file enforces:
//
//   - A purchase order has at most ONE open (draft) receipt at a time. Receiving again while one is
//     open reuses it instead of creating another, so a staff member who clicks "Mark Received"
//     while the first receipt is awaiting approval no longer piles up identical drafts.
//   - Approving a goods_receipt request posts the receipt right away (exactly once, guarded by
//     executed_at), the same way an approved stock adjustment or stock count posts.
//   - Rejecting a goods_receipt request cancels the draft receipt, so the order is free to be
//     received again with a fresh receipt and a fresh approval.
//   - A receipt is never posted against an order that is already fully received or cancelled, so a
//     stray duplicate draft can never double the stock.

// errOrderClosedForReceipt is returned when a receipt would post against an order that is already
// fully received or cancelled.
var errOrderClosedForReceipt = errors.New("purchase order is already fully received or cancelled")

// poAcceptsReceipts reports whether goods may still be received against an order in this status.
func poAcceptsReceipts(status string) bool {
	return status == "draft" || status == "sent" || status == "partially_received"
}

// openGoodsReceipts returns the order's draft receipts, oldest first. Ordering by created date
// then id gives every caller the same "first" receipt, which is what the duplicate guard in
// ReceivePurchaseOrder relies on.
func (h *InventoryExtrasHandler) openGoodsReceipts(ctx context.Context, tenantID, poID uuid.UUID) ([]*ent.GoodsReceipt, error) {
	return h.orm.GoodsReceipt.Query().
		Where(entgr.TenantID(tenantID), entgr.PurchaseOrderID(poID), entgr.StatusEQ(entgr.StatusDraft)).
		Order(ent.Asc(entgr.FieldCreatedAt), ent.Asc(entgr.FieldID)).
		All(ctx)
}

// liveOpenGoodsReceipts is openGoodsReceipts minus drafts whose approval was rejected. A rejected
// receipt is dead: it is cancelled here (it should already have been cancelled by the reject, this
// also tidies drafts rejected before that rule existed) so it never blocks or gets posted later.
func (h *InventoryExtrasHandler) liveOpenGoodsReceipts(ctx context.Context, tenantID, poID uuid.UUID) ([]*ent.GoodsReceipt, error) {
	open, err := h.openGoodsReceipts(ctx, tenantID, poID)
	if err != nil {
		return nil, err
	}
	live := make([]*ent.GoodsReceipt, 0, len(open))
	for _, g := range open {
		if latest, lerr := h.approvals().Latest(ctx, tenantID, g.ID); lerr == nil && latest != nil && latest.Status == areq.StatusRejected {
			if _, cerr := h.cancelDraftGoodsReceipt(ctx, tenantID, g, "approval rejected"); cerr != nil {
				h.log.Warn("cancel rejected goods receipt failed", zap.String("grn_id", g.ID.String()), zap.Error(cerr))
			}
			continue
		}
		live = append(live, g)
	}
	return live, nil
}

// cancelDraftGoodsReceipt moves a draft receipt to cancelled. Only a draft can be cancelled (a
// posted receipt has already moved stock and is reversed with a purchase return instead). Any
// still-pending approval request for it is withdrawn so approvers are not asked to sign off a
// receipt that no longer exists. Returns false when the receipt was not a draft any more.
func (h *InventoryExtrasHandler) cancelDraftGoodsReceipt(ctx context.Context, tenantID uuid.UUID, g *ent.GoodsReceipt, reason string) (bool, error) {
	n, err := h.orm.GoodsReceipt.Update().
		Where(entgr.ID(g.ID), entgr.TenantID(tenantID), entgr.StatusEQ(entgr.StatusDraft)).
		SetStatus(entgr.StatusCancelled).
		Save(ctx)
	if err != nil || n == 0 {
		return false, err
	}
	_, _ = h.orm.ApprovalRequest.Update().
		Where(areq.TenantID(tenantID), areq.ObjectID(g.ID), areq.StatusEQ(areq.StatusPending)).
		SetStatus(areq.StatusCancelled).
		Save(ctx)
	h.publishOutbox(ctx, tenantID, "inventory", g.ID, "goods_receipt.cancelled", map[string]any{
		"id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": g.PurchaseOrderID, "reason": reason,
	})
	return true, nil
}

// postGoodsReceiptOnce posts a draft receipt after its approval gate has passed. It refuses an
// order that is already fully received or cancelled, and marks an approved request as executed
// so the approval record shows the receipt was applied.
func (h *InventoryExtrasHandler) postGoodsReceiptOnce(ctx context.Context, tenantID uuid.UUID, g *ent.GoodsReceipt, po *ent.PurchaseOrder) error {
	if !poAcceptsReceipts(po.Status.String()) {
		return errOrderClosedForReceipt
	}
	if _, err := h.postGoodsReceiptCore(ctx, tenantID, g, po); err != nil {
		return err
	}
	if latest, err := h.approvals().Latest(ctx, tenantID, g.ID); err == nil && latest != nil &&
		latest.Status == areq.StatusApproved && latest.ExecutedAt == nil {
		_, _ = h.approvals().ClaimExecution(ctx, tenantID, latest.ID)
	}
	return nil
}

// executeApprovedGoodsReceipt posts the receipt behind a just-approved goods_receipt request.
// It claims executed_at first, so the receipt posts exactly once even if two approvals race;
// postGoodsReceiptCore's own draft-to-posted switch is a second guard against double stock.
// On failure the claim is released, and the receipt stays a draft that "Mark Received" (or the
// receipt's Post button) can finish later, since the approval is already in place.
func (h *InventoryExtrasHandler) executeApprovedGoodsReceipt(ctx context.Context, req *ent.ApprovalRequest) {
	g, err := h.orm.GoodsReceipt.Query().
		Where(entgr.ID(req.ObjectID), entgr.TenantID(req.TenantID)).
		Only(ctx)
	if err != nil {
		h.log.Error("approved goods receipt not found; nothing posted",
			zap.String("request_id", req.ID.String()), zap.String("grn_id", req.ObjectID.String()), zap.Error(err))
		return
	}
	if g.Status != entgr.StatusDraft {
		if g.Status == entgr.StatusPosted {
			// Already posted by another path: record that the approval has been applied.
			_, _ = h.approvals().ClaimExecution(ctx, req.TenantID, req.ID)
		}
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpo.ID(g.PurchaseOrderID), entpo.TenantID(req.TenantID)).
		WithSupplier().
		Only(ctx)
	if err != nil {
		h.log.Error("approved goods receipt has no purchase order; nothing posted",
			zap.String("grn_id", g.ID.String()), zap.Error(err))
		return
	}
	if !poAcceptsReceipts(po.Status.String()) {
		h.log.Warn("approved goods receipt not posted: its order is already received or cancelled",
			zap.String("grn", g.GrnNumber), zap.String("po", po.PoNumber), zap.String("po_status", po.Status.String()))
		return
	}
	won, err := h.approvals().ClaimExecution(ctx, req.TenantID, req.ID)
	if err != nil || !won {
		if err != nil {
			h.log.Error("claim goods receipt execution failed", zap.String("request_id", req.ID.String()), zap.Error(err))
		}
		return
	}
	if _, err := h.postGoodsReceiptCore(ctx, req.TenantID, g, po); err != nil {
		_ = h.approvals().ReleaseExecution(ctx, req.TenantID, req.ID)
		h.log.Error("post approved goods receipt failed", zap.String("grn", g.GrnNumber), zap.Error(err))
		return
	}
	h.log.Info("posted approved goods receipt", zap.String("grn", g.GrnNumber), zap.String("po", po.PoNumber))
	h.publishOutbox(ctx, req.TenantID, "approval_request", req.ID, "inventory.approval.executed", map[string]any{
		"id": req.ID, "module": req.Module, "goods_receipt_id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": po.ID,
	})
}

// onApprovalRejected undoes the pending effect of a rejected request where the module parked
// something waiting for sign-off. Today that is the goods receipt: its draft is cancelled so the
// order can be received again. Other modules keep their document as it was (a rejected purchase
// order stays a draft that can be amended and resubmitted).
func (h *InventoryExtrasHandler) onApprovalRejected(ctx context.Context, req *ent.ApprovalRequest) {
	if req.Module != areq.ModuleGoodsReceipt {
		return
	}
	g, err := h.orm.GoodsReceipt.Query().
		Where(entgr.ID(req.ObjectID), entgr.TenantID(req.TenantID)).
		Only(ctx)
	if err != nil {
		return
	}
	if _, err := h.cancelDraftGoodsReceipt(ctx, req.TenantID, g, "approval rejected"); err != nil {
		h.log.Warn("cancel rejected goods receipt failed", zap.String("grn", g.GrnNumber), zap.Error(err))
	}
}

// deleteGoodsReceipt removes a just-created draft and its lines (used when a concurrent receive
// already opened a receipt for the same order).
func (h *InventoryExtrasHandler) deleteGoodsReceipt(ctx context.Context, tenantID, grnID uuid.UUID) {
	_, _ = h.orm.GoodsReceiptLine.Delete().Where(entgrl.GoodsReceiptID(grnID), entgrl.TenantID(tenantID)).Exec(ctx)
	_, _ = h.orm.GoodsReceipt.Delete().Where(entgr.ID(grnID), entgr.TenantID(tenantID), entgr.StatusEQ(entgr.StatusDraft)).Exec(ctx)
}

// openReceiptDTO tells the purchase order screen that a receipt is already open, so it can show
// "Awaiting receipt approval" instead of offering to receive, amend or cancel again.
type openReceiptDTO struct {
	ID        uuid.UUID `json:"id"`
	GrnNumber string    `json:"grn_number"`
	// ApprovalStatus is the latest approval decision on the receipt: "pending", "approved", or
	// empty when the receipt needs no approval (or has not been submitted yet).
	ApprovalStatus string `json:"approval_status,omitempty"`
	// Count is how many draft receipts the order has. More than one only happens for data
	// created before the one-open-receipt rule and needs the extras cancelled.
	Count int `json:"count"`
}

// openReceiptFor summarises the order's open receipt for the detail view (nil when none).
func (h *InventoryExtrasHandler) openReceiptFor(ctx context.Context, tenantID, poID uuid.UUID) *openReceiptDTO {
	open, err := h.openGoodsReceipts(ctx, tenantID, poID)
	if err != nil || len(open) == 0 {
		return nil
	}
	out := &openReceiptDTO{ID: open[0].ID, GrnNumber: open[0].GrnNumber, Count: len(open)}
	if latest, lerr := h.approvals().Latest(ctx, tenantID, open[0].ID); lerr == nil && latest != nil {
		switch latest.Status {
		case areq.StatusPending, areq.StatusApproved:
			out.ApprovalStatus = string(latest.Status)
		}
	}
	return out
}

// CancelGoodsReceipt handles POST /inventory/goods-receipts/{grnID}/cancel. Only a draft can be
// cancelled; a posted receipt is reversed with a purchase return.
//
//	@Summary  Cancel a draft goods receipt
//	@Tags     Procurement
//	@Produce  json
//	@Param    grnID  path  string  true  "GRN ID"
//	@Success  200  {object}  grnDTO
//	@Failure  409  {object}  map[string]string
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/goods-receipts/{grnID}/cancel [post]
func (h *InventoryExtrasHandler) CancelGoodsReceipt(w http.ResponseWriter, r *http.Request) {
	tenantID, g, ok := h.loadGRN(w, r)
	if !ok {
		return
	}
	if g.Status != entgr.StatusDraft {
		writeError(w, http.StatusConflict, "INVALID_STATE", "Only a draft goods receipt can be cancelled. Use a purchase return to send back posted goods.")
		return
	}
	cancelled, err := h.cancelDraftGoodsReceipt(r.Context(), tenantID, g, "cancelled by user")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CANCEL_FAILED", "Failed to cancel goods receipt")
		return
	}
	if !cancelled {
		writeError(w, http.StatusConflict, "INVALID_STATE", "This goods receipt was posted or cancelled in the meantime")
		return
	}
	g.Status = entgr.StatusCancelled
	writeJSON(w, http.StatusOK, grnToDTO(g, nil))
}
