package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entact "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
)

// Approval-gated actions.
//
// Every effecting action under the approval matrix (ship a transfer, approve a purchase return,
// start a production batch, activate a contract, award an RFQ, complete an asset job, post a goods
// receipt, approve a stock count) goes through approvals.Service.Gate:
//
//   - The first press opens a request and answers 409 with state "submitted".
//   - A rejected (or cancelled, or changed) document is fixed and pressed again: a NEW request
//     opens (state "resubmitted"); the rejected request stays as history.
//   - Approving the request runs the action straight away through executeApprovedAction, exactly
//     once: executed_at is claimed before acting and released if the action fails, and every
//     action also switches its document's state with a conditional update so a second run finds
//     nothing to do.
//   - If the run failed, the request stays "approved, not executed" and pressing the action again
//     runs it on that approval (claiming it the same way). POST /approval-requests/{id}/execute
//     does the same from the approval itself.
//   - An executed approval is spent: pressing the action again later needs a new approval.

// SetTransferService wires the transfer service the approval executor ships transfers with.
func (h *InventoryExtrasHandler) SetTransferService(svc TransferServicer) { h.transferSvc = svc }

// actionError is a gated action that could not run. Handlers render it as the HTTP answer; the
// approval executor logs it.
type actionError struct {
	Status  int
	Code    string
	Message string
	// Body replaces the standard {code, message} answer when the action has more to say (the
	// material shortages of a production batch).
	Body map[string]any
	// Settled means the document can no longer take this action (it was already done, or is
	// closed), so an approval claim taken for it is kept instead of released.
	Settled bool
}

func (e *actionError) Error() string { return e.Code + ": " + e.Message }

func writeActionError(w http.ResponseWriter, e *actionError) {
	if e.Body != nil {
		writeJSON(w, e.Status, e.Body)
		return
	}
	writeError(w, e.Status, e.Code, e.Message)
}

// approvalAction carries out a gated action and returns the success body.
type approvalAction func(ctx context.Context) (any, *actionError)

// runClaimed runs act under the execution claim of an approved request. With no request (no
// approval needed) it just runs act. The claim is a compare-and-set on executed_at, so when the
// executor and a manual press race only one of them acts; a failed action releases the claim
// unless the document is settled.
func runClaimed(ctx context.Context, svc *approvals.Service, req *ent.ApprovalRequest, act approvalAction) (any, *actionError) {
	if req == nil {
		return act(ctx)
	}
	won, err := svc.ClaimExecution(ctx, req.TenantID, req.ID)
	if err != nil {
		return nil, &actionError{Status: http.StatusInternalServerError, Code: "CLAIM_FAILED", Message: "Could not record the approval as used. Try again."}
	}
	if !won {
		return nil, &actionError{Status: http.StatusConflict, Code: "ALREADY_EXECUTED", Message: "This approval has already been used or was cancelled.", Settled: true}
	}
	out, aerr := act(ctx)
	if aerr != nil && !aerr.Settled {
		_ = svc.ReleaseExecution(ctx, req.TenantID, req.ID)
	}
	return out, aerr
}

// writeApprovalGate answers a gate result that does not let the action proceed. The body keeps the
// standard code and message and adds approval_required, state, module and request_id, so the app
// can tell "submitted", "resubmitted", "pending" and "rejected" apart.
func writeApprovalGate(w http.ResponseWriter, module string, res approvals.GateResult) {
	code, msg := "APPROVAL_REQUIRED", "This action requires approval before it can proceed."
	switch res.State {
	case approvals.StatePending:
		code, msg = "APPROVAL_PENDING", "This action is awaiting approval sign-off."
	case approvals.StateSubmitted:
		msg = "This action requires approval. It has been submitted for sign-off."
	case approvals.StateResubmitted:
		msg = "This action has been sent for approval again."
	case approvals.StateRejected:
		code, msg = "APPROVAL_REJECTED", "The approval request was rejected. Fix the document and submit it again."
	}
	body := map[string]any{
		"code": code, "error": code, "message": msg,
		"approval_required": true, "state": res.State, "module": module,
	}
	if res.Request != nil {
		body["request_id"] = res.Request.ID
	}
	writeJSON(w, http.StatusConflict, body)
}

// writeApprovalUnavailable answers a gate that could not decide (the rule lookup, the request
// lookup or the request write failed). The action does not run: an approval that may apply is
// never skipped because the database hiccuped.
func writeApprovalUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "APPROVAL_UNAVAILABLE", "The approval check could not be completed, so nothing was done. Try again in a moment.")
}

// gateApprovalWith runs the approval gate for an action. It returns ok=true when the action may
// proceed, with the approved request the caller must claim (nil when no approval is needed). When
// the action must wait it writes the 409 and returns ok=false; a new or renewed request also
// queues inventory.approval.submitted so approvers are told. payload is stored on a new request:
// the inputs the executor needs to run the action on approval. The gate fails CLOSED: any error
// (reading the rule or the requests, or writing the new request) answers 503 and the action does
// not run. Only "no rule matches" lets an action through without approval.
func (h *InventoryExtrasHandler) gateApprovalWith(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, module string, objectID uuid.UUID, reference string, amount float64, payload map[string]any) (*ent.ApprovalRequest, bool) {
	var submittedBy *uuid.UUID
	if uid, ok := actingUserID(r); ok {
		submittedBy = &uid
	}
	res, err := h.approvals().Gate(r.Context(), tenantID, module, objectID, reference, amount, submittedBy, payload)
	if err != nil {
		h.log.Error("approval gate failed; action refused", zap.String("module", module),
			zap.String("object_id", objectID.String()), zap.Error(err))
		writeApprovalUnavailable(w)
		return nil, false
	}
	if res.Proceed() {
		if res.State == approvals.StateApproved {
			return res.Request, true
		}
		return nil, true
	}
	if res.State == approvals.StateSubmitted || res.State == approvals.StateResubmitted {
		h.publishOutbox(r.Context(), tenantID, "approval_request", res.Request.ID, "inventory.approval.submitted", map[string]any{
			"id": res.Request.ID, "module": module, "object_id": objectID, "amount": amount,
			"resubmitted": res.State == approvals.StateResubmitted,
		})
	}
	writeApprovalGate(w, module, res)
	return nil, false
}

// gateApproval is gateApprovalWith for callers that claim the approval themselves (goods receipts
// mark it executed when the receipt posts).
func (h *InventoryExtrasHandler) gateApproval(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, module string, objectID uuid.UUID, reference string, amount float64) bool {
	_, ok := h.gateApprovalWith(w, r, tenantID, module, objectID, reference, amount, nil)
	return ok
}

// runGatedAction is the whole press-the-button path: gate, claim the approval when there is one,
// run the action, answer 200 with its body or the action's error.
func (h *InventoryExtrasHandler) runGatedAction(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, module string, objectID uuid.UUID, reference string, amount float64, payload map[string]any, act approvalAction) {
	approved, ok := h.gateApprovalWith(w, r, tenantID, module, objectID, reference, amount, payload)
	if !ok {
		return
	}
	out, aerr := runClaimed(r.Context(), h.approvals(), approved, act)
	if aerr != nil {
		writeActionError(w, aerr)
		return
	}
	if approved != nil {
		h.publishApprovalExecuted(r.Context(), approved)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *InventoryExtrasHandler) publishApprovalExecuted(ctx context.Context, req *ent.ApprovalRequest) {
	h.publishOutbox(ctx, req.TenantID, "approval_request", req.ID, "inventory.approval.executed", map[string]any{
		"id": req.ID, "module": req.Module, "object_id": req.ObjectID,
	})
}

// approvedActionFor builds the action an approved request runs for the document-based modules.
// Each loads its document inside the tenant and switches it with a conditional update. Returns
// (nil, nil) for modules that have no deferred action here.
func (h *InventoryExtrasHandler) approvedActionFor(req *ent.ApprovalRequest, approver uuid.UUID) (approvalAction, *actionError) {
	tenantID, objectID := req.TenantID, req.ObjectID
	switch req.Module {
	case areq.ModuleStockTransfer:
		if h.transferSvc == nil {
			return nil, &actionError{Status: http.StatusServiceUnavailable, Code: "NOT_WIRED", Message: "Transfers are not available to the approval executor."}
		}
		return shipTransferAction(h.transferSvc, tenantID, objectID), nil
	case areq.ModulePurchaseReturn:
		return func(ctx context.Context) (any, *actionError) {
			pr, err := h.orm.PurchaseReturn.Get(ctx, objectID)
			if err != nil || pr.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Purchase return not found", Settled: true}
			}
			return h.approvePurchaseReturnCore(ctx, tenantID, pr, approver)
		}, nil
	case areq.ModuleProductionBatch:
		force, _ := req.Payload["force"].(bool)
		return func(ctx context.Context) (any, *actionError) {
			b, err := h.orm.ProductionBatch.Get(ctx, objectID)
			if err != nil || b.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Production batch not found", Settled: true}
			}
			return h.startProductionBatchCore(ctx, tenantID, b, force)
		}, nil
	case areq.ModuleContract:
		return func(ctx context.Context) (any, *actionError) {
			c, err := h.orm.Contract.Get(ctx, objectID)
			if err != nil || c.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Contract not found", Settled: true}
			}
			return h.activateContractCore(ctx, tenantID, c)
		}, nil
	case areq.ModuleRfq:
		awards, ok := awardsFromPayload(req.Payload)
		if !ok {
			// Requests opened before the selection was stored cannot be replayed; award again.
			return nil, &actionError{Status: http.StatusConflict, Code: "NO_AWARD_SELECTION", Message: "This approval does not carry the award selection. Award the RFQ again to submit it."}
		}
		return func(ctx context.Context) (any, *actionError) {
			return h.awardRFQCore(ctx, tenantID, objectID, req.ObjectReference, awards)
		}, nil
	case areq.ModuleAssetMaintenance:
		return func(ctx context.Context) (any, *actionError) {
			rec, err := h.orm.AssetMaintenance.Get(ctx, objectID)
			if err != nil || rec.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Maintenance record not found", Settled: true}
			}
			return h.completeAssetMaintenanceCore(ctx, tenantID, rec)
		}, nil
	case areq.ModuleAssetTransfer:
		return func(ctx context.Context) (any, *actionError) {
			rec, err := h.orm.AssetTransfer.Get(ctx, objectID)
			if err != nil || rec.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Transfer not found", Settled: true}
			}
			return h.completeAssetTransferCore(ctx, tenantID, rec)
		}, nil
	case areq.ModuleAssetDisposal:
		return func(ctx context.Context) (any, *actionError) {
			rec, err := h.orm.AssetDisposal.Get(ctx, objectID)
			if err != nil || rec.TenantID != tenantID {
				return nil, &actionError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Disposal not found", Settled: true}
			}
			return h.completeAssetDisposalCore(ctx, tenantID, rec)
		}, nil
	}
	return nil, nil
}

// runApprovedDocumentAction is the executor for the document-based modules: build the action,
// run it under the claim, publish inventory.approval.executed on success.
func (h *InventoryExtrasHandler) runApprovedDocumentAction(ctx context.Context, req *ent.ApprovalRequest, approver uuid.UUID) *actionError {
	act, aerr := h.approvedActionFor(req, approver)
	if aerr == nil && act == nil {
		return nil
	}
	if aerr == nil {
		_, aerr = runClaimed(ctx, h.approvals(), req, act)
	}
	if aerr != nil {
		h.log.Warn("approved action not carried out",
			zap.String("module", string(req.Module)), zap.String("request_id", req.ID.String()),
			zap.String("object_id", req.ObjectID.String()), zap.String("code", aerr.Code), zap.String("reason", aerr.Message))
		return aerr
	}
	h.log.Info("approved action carried out", zap.String("module", string(req.Module)), zap.String("request_id", req.ID.String()))
	h.publishApprovalExecuted(ctx, req)
	return nil
}

// ExecuteApprovalRequest handles POST /inventory/approval-requests/{reqID}/execute. It runs the
// action of an approved request whose action never ran (the run at approval time failed, or the
// request was approved before the executor covered its module). The same claim guards it, so the
// action still happens at most once. Purchase orders and requisitions have nothing to run here:
// an approved order is sent with Send to supplier.
//
//	@Summary  Run the action of an approved request that has not run yet
//	@Tags     Approvals
//	@Produce  json
//	@Param    reqID  path  string  true  "Approval request ID"
//	@Success  200  {object}  approvalRequestDTO
//	@Failure  404  {object}  map[string]string
//	@Failure  409  {object}  map[string]string
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/approval-requests/{reqID}/execute [post]
func (h *InventoryExtrasHandler) ExecuteApprovalRequest(w http.ResponseWriter, r *http.Request) {
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
	userID, ok := actingUserID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	req, err := h.orm.ApprovalRequest.Query().Where(areq.ID(id), areq.TenantID(tenantID)).WithActions().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Approval request not found")
		return
	}
	before := approvalSnapshot(req, req.Edges.Actions)
	switch {
	case req.Status != areq.StatusApproved:
		writeError(w, http.StatusConflict, "NOT_APPROVED", "Only an approved request can be carried out")
		return
	case req.ExecutedAt != nil:
		writeError(w, http.StatusConflict, "ALREADY_EXECUTED", "This approved action has already been carried out")
		return
	case req.Module == areq.ModulePurchaseOrder || req.Module == areq.ModuleRequisition:
		writeError(w, http.StatusConflict, "NOTHING_TO_EXECUTE", "Nothing runs from this approval. An approved purchase order is sent from the order with Send to supplier.")
		return
	}
	// The action is carried out on the approver's sign-off, so documents that record an approver
	// (a stock count) get the person who approved, not the person retrying.
	approver := finalApprover(req)
	if approver == uuid.Nil {
		approver = userID
	}
	if aerr := h.executeApprovedAction(r.Context(), req, approver); aerr != nil {
		writeActionError(w, aerr)
		return
	}
	full, err := h.orm.ApprovalRequest.Query().Where(areq.ID(req.ID)).WithActions().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to reload approval request")
		return
	}
	if full.ExecutedAt == nil {
		writeError(w, http.StatusConflict, "NOT_EXECUTED", "The approved action could not be carried out. Check the document and try again.")
		return
	}
	// Record who ran it by hand. Best effort: the action already happened and must not be undone
	// because the audit write failed.
	before["approved_by"] = approver.String()
	if aerr := writeAuditTx(r.Context(), h.orm, tenantID, userID, "approval.executed_manually", "approval_request", req.ID.String(), "", before); aerr != nil {
		h.log.Warn("audit of manual approval execution failed", zap.String("request_id", req.ID.String()), zap.Error(aerr))
	}
	writeJSON(w, http.StatusOK, approvalRequestToDTO(full))
}

// finalApprover is the user who approved the request's last approved step (uuid.Nil when none is
// recorded).
func finalApprover(req *ent.ApprovalRequest) uuid.UUID {
	var (
		who uuid.UUID
		seq = -1
	)
	for _, a := range req.Edges.Actions {
		if a.Status == entact.StatusApproved && a.ActedBy != nil && a.Sequence > seq {
			who, seq = *a.ActedBy, a.Sequence
		}
	}
	return who
}
