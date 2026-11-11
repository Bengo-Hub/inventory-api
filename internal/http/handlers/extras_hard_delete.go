package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entact "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entcol "github.com/bengobox/inventory-service/internal/ent/contractorderlink"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entserial "github.com/bengobox/inventory-service/internal/ent/inventoryserial"
	entppc "github.com/bengobox/inventory-service/internal/ent/pendingpricechange"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpol "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entaward "github.com/bengobox/inventory-service/internal/ent/rfqaward"
)

// Hard delete for platform owners.
//
// Duplicate approvals and purchase orders created by mistake used to be removed by hand in SQL.
// These two endpoints do it through the API instead, with the same guard rails every time:
//
//   - Only a caller whose signed token carries is_platform_owner may use them. Tenant admins and
//     the superuser role are refused, and the tenant slug is never consulted.
//   - Nothing that has already changed stock or been executed is deleted, because deleting the
//     record of an applied action hides history. Those cases answer 409.
//   - Everything is removed in one transaction together with an audit_logs row holding a
//     snapshot of what was deleted.

// platformOwnerCaller returns the caller's user id when the signed claims mark them as platform
// owner. Roles (admin, superuser) never qualify.
func platformOwnerCaller(r *http.Request) (uuid.UUID, bool) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims == nil || !claims.IsPlatformOwner {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// hardDeleteError is a refusal decided inside the delete transaction.
type hardDeleteError struct {
	status  int
	code    string
	message string
}

func (e *hardDeleteError) Error() string { return e.message }

func refuse(status int, code, message string) *hardDeleteError {
	return &hardDeleteError{status: status, code: code, message: message}
}

// approvalSnapshot is the before_json written to the audit row for a deleted request.
func approvalSnapshot(req *ent.ApprovalRequest, actions []*ent.ApprovalAction) map[string]any {
	snap := map[string]any{
		"id": req.ID.String(), "module": string(req.Module), "object_id": req.ObjectID.String(),
		"object_reference": req.ObjectReference, "amount": req.Amount, "status": string(req.Status),
		"current_sequence": req.CurrentSequence, "created_at": req.CreatedAt,
	}
	if req.RuleID != nil {
		snap["rule_id"] = req.RuleID.String()
	}
	if req.SubmittedBy != nil {
		snap["submitted_by"] = req.SubmittedBy.String()
	}
	if req.DecidedAt != nil {
		snap["decided_at"] = *req.DecidedAt
	}
	acts := make([]map[string]any, 0, len(actions))
	for _, a := range actions {
		m := map[string]any{"sequence": a.Sequence, "name": a.Name, "approver_role": a.ApproverRole, "status": string(a.Status)}
		if a.ActedBy != nil {
			m["acted_by"] = a.ActedBy.String()
		}
		if a.ActedAt != nil {
			m["acted_at"] = *a.ActedAt
		}
		acts = append(acts, m)
	}
	snap["actions"] = acts
	return snap
}

// writeAuditTx writes an audit row inside the delete transaction, so the record of the delete
// exists exactly when the delete happened. reason is the optional ?reason= the caller gave.
func writeAuditTx(ctx context.Context, c *ent.Client, tenantID, actor uuid.UUID, action, entityType, entityID, reason string, before map[string]any) error {
	b := c.AuditLog.Create().
		SetTenantID(tenantID).SetActorUserID(actor).SetAction(action).
		SetEntityType(entityType).SetEntityID(entityID).SetBeforeJSON(before)
	if reason != "" {
		b = b.SetReason(reason)
	}
	_, err := b.Save(ctx)
	return err
}

// deleteReason reads the optional free-text reason, capped so the audit row stays small.
func deleteReason(r *http.Request) string {
	// Cut on characters, not bytes, and drop invalid bytes: Postgres refuses text that is not
	// valid UTF-8, which would fail the whole delete.
	reason := strings.ToValidUTF8(strings.TrimSpace(r.URL.Query().Get("reason")), "")
	if runes := []rune(reason); len(runes) > 500 {
		reason = string(runes[:500])
	}
	return reason
}

// receiptLinesReferenced reports whether stock records (lots, serials, pending price changes)
// point at any of the given receipt lines. A draft never has them; if one does, it is not safe
// to delete.
func receiptLinesReferenced(ctx context.Context, c *ent.Client, tenantID uuid.UUID, lineIDs []uuid.UUID) (bool, error) {
	if len(lineIDs) == 0 {
		return false, nil
	}
	if ok, err := c.InventoryLot.Query().Where(entlot.TenantID(tenantID), entlot.GoodsReceiptLineIDIn(lineIDs...)).Exist(ctx); err != nil || ok {
		return ok, err
	}
	if ok, err := c.InventorySerial.Query().Where(entserial.TenantID(tenantID), entserial.GoodsReceiptLineIDIn(lineIDs...)).Exist(ctx); err != nil || ok {
		return ok, err
	}
	return c.PendingPriceChange.Query().Where(entppc.TenantID(tenantID), entppc.GoodsReceiptLineIDIn(lineIDs...)).Exist(ctx)
}

// The delete steps below carry their guard in the DELETE itself and compare the affected row
// count. The guards in the handlers are read with a plain SELECT first (for a clear message),
// but an approval or a post can commit between that read and the delete. Postgres re-checks a
// DELETE's WHERE clause after waiting on the row lock, so a row that changed meanwhile is not
// deleted, the count comes up short, and the refusal rolls the whole transaction back.

// deleteApprovalRequestsTx deletes the given requests and their actions, but only requests that
// were never executed. Approval steps belong to rules, not requests, so they are left alone.
func deleteApprovalRequestsTx(ctx context.Context, c *ent.Client, tenantID uuid.UUID, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := c.ApprovalAction.Delete().Where(entact.TenantID(tenantID), entact.RequestIDIn(ids...)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	gone, err := c.ApprovalRequest.Delete().
		Where(areq.TenantID(tenantID), areq.IDIn(ids...), areq.ExecutedAtIsNil()).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	if gone != len(ids) {
		return 0, refuse(http.StatusConflict, "APPROVAL_EXECUTED", "An approval was executed while deleting, so nothing was deleted.")
	}
	return n, nil
}

// deleteReceiptsTx deletes receipts and their lines after checking that nothing else (stock
// records, purchase returns) points at them. Only receipts still in one of the allowed statuses
// are deleted; if any receipt changed status meanwhile, nothing is.
func deleteReceiptsTx(ctx context.Context, c *ent.Client, tenantID uuid.UUID, receiptIDs []uuid.UUID, allowed ...entgr.Status) error {
	if len(receiptIDs) == 0 {
		return nil
	}
	lineIDs, err := c.GoodsReceiptLine.Query().Where(entgrl.TenantID(tenantID), entgrl.GoodsReceiptIDIn(receiptIDs...)).IDs(ctx)
	if err != nil {
		return err
	}
	if used, err := receiptLinesReferenced(ctx, c, tenantID, lineIDs); err != nil {
		return err
	} else if used {
		return refuse(http.StatusConflict, "RECEIPT_REFERENCED", "Stock records point at this receipt, so it cannot be deleted.")
	}
	if ok, err := c.PurchaseReturn.Query().Where(entpr.TenantID(tenantID), entpr.GoodsReceiptIDIn(receiptIDs...)).Exist(ctx); err != nil {
		return err
	} else if ok {
		return refuse(http.StatusConflict, "HAS_PURCHASE_RETURN", "A purchase return was raised from this receipt, so it cannot be deleted.")
	}
	if _, err := c.GoodsReceiptLine.Delete().Where(entgrl.TenantID(tenantID), entgrl.GoodsReceiptIDIn(receiptIDs...)).Exec(ctx); err != nil {
		return err
	}
	gone, err := c.GoodsReceipt.Delete().
		Where(entgr.TenantID(tenantID), entgr.IDIn(receiptIDs...), entgr.StatusIn(allowed...)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if gone != len(receiptIDs) {
		if posted, _ := c.GoodsReceipt.Query().Where(entgr.TenantID(tenantID), entgr.IDIn(receiptIDs...), entgr.StatusEQ(entgr.StatusPosted)).Exist(ctx); posted {
			return refuse(http.StatusConflict, "RECEIPT_POSTED", "A goods receipt was posted while deleting, so nothing was deleted.")
		}
		return refuse(http.StatusConflict, "RECEIPT_NOT_DRAFT", "A goods receipt changed status while deleting, so nothing was deleted.")
	}
	return nil
}

// deletePurchaseOrderRowTx deletes the order's lines and the order itself, but only while no
// line shows received stock and the order is not received or partially received.
func deletePurchaseOrderRowTx(ctx context.Context, c *ent.Client, tenantID, poID uuid.UUID) error {
	stockIn := refuse(http.StatusConflict, "RECEIPT_POSTED", "Stock was received against this order, so it cannot be deleted.")
	if _, err := c.PurchaseOrderLine.Delete().Where(entpol.PoID(poID), entpol.QuantityReceivedLTE(0)).Exec(ctx); err != nil {
		return err
	}
	// Any line left is one that received stock (now or meanwhile).
	if left, err := c.PurchaseOrderLine.Query().Where(entpol.PoID(poID)).Exist(ctx); err != nil {
		return err
	} else if left {
		return stockIn
	}
	gone, err := c.PurchaseOrder.Delete().
		Where(entpo.ID(poID), entpo.TenantID(tenantID),
			entpo.StatusNotIn(entpo.StatusReceived, entpo.StatusPartiallyReceived)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if gone != 1 {
		return stockIn
	}
	return nil
}

// runHardDelete runs fn in a transaction and maps refusals to their HTTP answer.
func (h *InventoryExtrasHandler) runHardDelete(w http.ResponseWriter, r *http.Request, fn func(c *ent.Client) error) bool {
	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TX_FAILED", "Failed to start the delete")
		return false
	}
	if err := fn(tx.Client()); err != nil {
		_ = tx.Rollback()
		var hd *hardDeleteError
		if errors.As(err, &hd) {
			writeError(w, hd.status, hd.code, hd.message)
			return false
		}
		h.log.Error("hard delete failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete")
		return false
	}
	if err := tx.Commit(); err != nil {
		h.log.Error("hard delete commit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete")
		return false
	}
	return true
}

// HardDeleteApprovalRequest handles DELETE /inventory/approval-requests/{reqID}.
//
// Platform owners only. Deletes the request and its actions. Refused (409) when the request was
// executed, when it is approved (its effect may have been applied), or when the object it is
// about has been posted. For a goods_receipt request whose receipt is still a draft,
// ?with_object=true also deletes that receipt, its lines and every other request about it.
//
//	@Summary  Permanently delete an approval request (platform owners only)
//	@Tags     Approvals
//	@Produce  json
//	@Param    reqID        path   string  true   "Approval request ID"
//	@Param    with_object  query  bool    false  "Also delete the draft goods receipt"
//	@Success  200  {object}  map[string]interface{}
//	@Failure  403  {object}  map[string]string
//	@Failure  409  {object}  map[string]string
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/approval-requests/{reqID} [delete]
func (h *InventoryExtrasHandler) HardDeleteApprovalRequest(w http.ResponseWriter, r *http.Request) {
	actor, ok := platformOwnerCaller(r)
	if !ok {
		writeError(w, http.StatusForbidden, "PLATFORM_OWNER_ONLY", "Only a platform owner can permanently delete approvals")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "reqID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid request ID")
		return
	}
	withObject := strings.EqualFold(r.URL.Query().Get("with_object"), "true")
	reason := deleteReason(r)

	var (
		req           *ent.ApprovalRequest
		actionsGone   int
		objectDeleted bool
	)
	done := h.runHardDelete(w, r, func(c *ent.Client) error {
		ctx := r.Context()
		req, err = c.ApprovalRequest.Query().Where(areq.ID(id), areq.TenantID(tenantID)).WithActions().Only(ctx)
		if ent.IsNotFound(err) {
			return refuse(http.StatusNotFound, "NOT_FOUND", "Approval request not found")
		}
		if err != nil {
			return err
		}
		if req.ExecutedAt != nil {
			return refuse(http.StatusConflict, "APPROVAL_EXECUTED", "This approval was executed; deleting it would hide what was applied.")
		}

		var receipt *ent.GoodsReceipt
		if req.Module == areq.ModuleGoodsReceipt {
			receipt, err = c.GoodsReceipt.Query().Where(entgr.ID(req.ObjectID), entgr.TenantID(tenantID)).Only(ctx)
			if err != nil && !ent.IsNotFound(err) {
				return err
			}
			if receipt != nil && receipt.Status == entgr.StatusPosted {
				return refuse(http.StatusConflict, "OBJECT_POSTED", "Goods receipt "+receipt.GrnNumber+" is posted; its approval is history and stays.")
			}
		} else if req.Status == areq.StatusApproved {
			// Outside goods receipts there is no reliable "not applied yet" signal for an
			// approved request (the gated action may have been repeated after approval).
			return refuse(http.StatusConflict, "APPROVAL_APPLIED", "This approval was granted and may have been acted on, so it stays.")
		}

		ids := []uuid.UUID{req.ID}
		var receiptIDs []uuid.UUID
		if withObject {
			if req.Module != areq.ModuleGoodsReceipt {
				return refuse(http.StatusBadRequest, "WITH_OBJECT_UNSUPPORTED", "with_object is only available for goods receipt approvals")
			}
			if receipt == nil || receipt.Status != entgr.StatusDraft {
				return refuse(http.StatusConflict, "RECEIPT_NOT_DRAFT", "Only a draft goods receipt can be deleted with its approval.")
			}
			// Every request about this receipt goes with it; none may have been executed.
			others, err := c.ApprovalRequest.Query().Where(areq.TenantID(tenantID), areq.ObjectID(receipt.ID), areq.IDNEQ(req.ID)).All(ctx)
			if err != nil {
				return err
			}
			for _, o := range others {
				if o.ExecutedAt != nil {
					return refuse(http.StatusConflict, "APPROVAL_EXECUTED", "Another approval for this receipt was executed, so the receipt stays.")
				}
				ids = append(ids, o.ID)
			}
			receiptIDs = []uuid.UUID{receipt.ID}
		}

		if actionsGone, err = deleteApprovalRequestsTx(ctx, c, tenantID, ids); err != nil {
			return err
		}
		if err := deleteReceiptsTx(ctx, c, tenantID, receiptIDs, entgr.StatusDraft); err != nil {
			return err
		}
		objectDeleted = len(receiptIDs) > 0
		before := approvalSnapshot(req, req.Edges.Actions)
		if objectDeleted {
			before["deleted_receipt"] = map[string]any{"id": receipt.ID.String(), "grn_number": receipt.GrnNumber, "status": string(receipt.Status)}
			before["deleted_request_ids"] = ids
		}
		return writeAuditTx(ctx, c, tenantID, actor, "approval.hard_deleted", "approval_request", req.ID.String(), reason, before)
	})
	if !done {
		return
	}
	h.log.Info("approval request hard deleted",
		zap.String("tenant_id", tenantID.String()), zap.String("request_id", req.ID.String()),
		zap.String("module", string(req.Module)), zap.String("object_id", req.ObjectID.String()),
		zap.Bool("object_deleted", objectDeleted), zap.String("actor", actor.String()))
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true, "id": req.ID, "actions_deleted": actionsGone, "object_deleted": objectDeleted,
	})
}

// HardDeletePurchaseOrder handles DELETE /inventory/purchase-orders/{poID}?hard=true.
//
// Platform owners only. Refused (409) when stock came in against the order: a posted goods
// receipt, a received or partially received status, or any line with quantity received (older
// orders were partly received and then cancelled before receipts existed). Inventory cannot see
// treasury bills; bills come from received stock, which these checks cover. Also refused when a
// purchase return exists or one of its approvals was executed. Otherwise one transaction deletes
// the order, its lines, its draft and cancelled receipts with their lines, every approval request
// (and actions) for the order and those receipts, and the order's contract links, clears RFQ
// award links, writes an audit row (optional ?reason=) and queues
// inventory.purchase_order.cancelled (ids only) so treasury releases any budget commitment.
//
//	@Summary  Permanently delete a purchase order (platform owners only)
//	@Tags     Procurement
//	@Produce  json
//	@Param    poID    path   string  true   "Purchase order ID"
//	@Param    hard    query  bool    true   "Must be true"
//	@Param    reason  query  string  false  "Why, kept on the audit row"
//	@Success  200  {object}  map[string]interface{}
//	@Failure  403  {object}  map[string]string
//	@Failure  409  {object}  map[string]string
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/purchase-orders/{poID} [delete]
func (h *InventoryExtrasHandler) HardDeletePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	actor, ok := platformOwnerCaller(r)
	if !ok {
		writeError(w, http.StatusForbidden, "PLATFORM_OWNER_ONLY", "Only a platform owner can permanently delete purchase orders")
		return
	}
	if !strings.EqualFold(r.URL.Query().Get("hard"), "true") {
		writeError(w, http.StatusBadRequest, "HARD_REQUIRED", "Pass hard=true to permanently delete; use cancel otherwise")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	reason := deleteReason(r)

	var (
		po            *ent.PurchaseOrder
		receiptsGone  int
		approvalsGone int
	)
	done := h.runHardDelete(w, r, func(c *ent.Client) error {
		ctx := r.Context()
		stockIn := refuse(http.StatusConflict, "RECEIPT_POSTED", "Stock was received against this order, so it cannot be deleted.")
		po, err = c.PurchaseOrder.Query().Where(entpo.ID(poID), entpo.TenantID(tenantID)).Only(ctx)
		if ent.IsNotFound(err) {
			return refuse(http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		}
		if err != nil {
			return err
		}
		if po.Status == entpo.StatusReceived || po.Status == entpo.StatusPartiallyReceived {
			return stockIn
		}
		lines, err := c.PurchaseOrderLine.Query().Where(entpol.PoID(po.ID)).All(ctx)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.QuantityReceived > 0 {
				return stockIn
			}
		}
		receipts, err := c.GoodsReceipt.Query().Where(entgr.TenantID(tenantID), entgr.PurchaseOrderID(po.ID)).All(ctx)
		if err != nil {
			return err
		}
		receiptIDs := make([]uuid.UUID, 0, len(receipts))
		grns := make([]string, 0, len(receipts))
		for _, g := range receipts {
			if g.Status == entgr.StatusPosted {
				return refuse(http.StatusConflict, "RECEIPT_POSTED", "Goods receipt "+g.GrnNumber+" is posted, so this order cannot be deleted.")
			}
			receiptIDs = append(receiptIDs, g.ID)
			grns = append(grns, g.GrnNumber)
		}
		if ok, err := c.PurchaseReturn.Query().Where(entpr.TenantID(tenantID), entpr.PurchaseOrderID(po.ID)).Exist(ctx); err != nil {
			return err
		} else if ok {
			return refuse(http.StatusConflict, "HAS_PURCHASE_RETURN", "A purchase return exists for this order, so it cannot be deleted.")
		}

		objects := append([]uuid.UUID{po.ID}, receiptIDs...)
		reqs, err := c.ApprovalRequest.Query().Where(areq.TenantID(tenantID), areq.ObjectIDIn(objects...)).All(ctx)
		if err != nil {
			return err
		}
		reqIDs := make([]uuid.UUID, 0, len(reqs))
		for _, rq := range reqs {
			if rq.ExecutedAt != nil {
				return refuse(http.StatusConflict, "APPROVAL_EXECUTED", "An approval for this order was executed, so it cannot be deleted.")
			}
			reqIDs = append(reqIDs, rq.ID)
		}

		// Each delete below re-applies its guard and checks the row count (see the note above
		// deleteApprovalRequestsTx).
		if _, err := deleteApprovalRequestsTx(ctx, c, tenantID, reqIDs); err != nil {
			return err
		}
		if err := deleteReceiptsTx(ctx, c, tenantID, receiptIDs, entgr.StatusDraft, entgr.StatusCancelled); err != nil {
			return err
		}
		if _, err := c.ContractOrderLink.Delete().Where(entcol.TenantID(tenantID), entcol.PurchaseOrderID(po.ID)).Exec(ctx); err != nil {
			return err
		}
		if _, err := c.RFQAward.Update().Where(entaward.TenantID(tenantID), entaward.PoID(po.ID)).ClearPoID().Save(ctx); err != nil {
			return err
		}
		if err := deletePurchaseOrderRowTx(ctx, c, tenantID, po.ID); err != nil {
			return err
		}
		receiptsGone, approvalsGone = len(receiptIDs), len(reqIDs)

		lineSnap := make([]map[string]any, 0, len(lines))
		for _, l := range lines {
			lineSnap = append(lineSnap, map[string]any{
				"item_id": l.ItemID.String(), "quantity_ordered": l.QuantityOrdered, "unit_price": l.UnitPrice,
			})
		}
		before := map[string]any{
			"id": po.ID.String(), "po_number": po.PoNumber, "status": string(po.Status),
			"total_amount": po.TotalAmount, "currency": po.Currency, "lines": lineSnap,
			"created_at": po.CreatedAt, "deleted_receipts": grns, "deleted_approval_request_ids": reqIDs,
		}
		if po.SupplierID != nil {
			before["supplier_id"] = po.SupplierID.String()
		}
		if po.WarehouseID != nil {
			before["warehouse_id"] = po.WarehouseID.String()
		}
		if err := writeAuditTx(ctx, c, tenantID, actor, "purchase_order.hard_deleted", "purchase_order", po.ID.String(), reason, before); err != nil {
			return err
		}
		// Treasury releases any budget commitment on this event. Ids only.
		return createOutboxEvent(ctx, c, tenantID, "purchase_order", po.ID, "inventory.purchase_order.cancelled", map[string]any{
			"tenant_id": tenantID, "po_id": po.ID, "status": "cancelled", "hard_deleted": true,
			"deleted_at": time.Now().UTC().Format(time.RFC3339),
		})
	})
	if !done {
		return
	}
	h.log.Info("purchase order hard deleted",
		zap.String("tenant_id", tenantID.String()), zap.String("po_id", po.ID.String()),
		zap.Int("receipts_deleted", receiptsGone), zap.Int("approvals_deleted", approvalsGone),
		zap.String("actor", actor.String()))
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true, "id": po.ID, "receipts_deleted": receiptsGone, "approvals_deleted": approvalsGone,
	})
}
