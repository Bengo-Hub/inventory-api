package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	astep "github.com/bengobox/inventory-service/internal/ent/approvalstep"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// approvals returns an approval engine bound to this handler's Ent client.
func (h *InventoryExtrasHandler) approvals() *approvals.Service {
	return approvals.NewService(h.orm)
}

// ─── DTOs ────────────────────────────────────────────────────────────────────

type approvalStepDTO struct {
	ID           uuid.UUID `json:"id"`
	Sequence     int       `json:"sequence"`
	Name         string    `json:"name"`
	ApproverRole string    `json:"approver_role"`
}

type approvalRuleDTO struct {
	ID        uuid.UUID         `json:"id"`
	Module    string            `json:"module"`
	Name      string            `json:"name"`
	MinAmount float64           `json:"min_amount"`
	MaxAmount *float64          `json:"max_amount"`
	IsActive  bool              `json:"is_active"`
	Steps     []approvalStepDTO `json:"steps"`
}

type approvalActionDTO struct {
	ID           uuid.UUID  `json:"id"`
	Sequence     int        `json:"sequence"`
	Name         string     `json:"name"`
	ApproverRole string     `json:"approver_role"`
	Status       string     `json:"status"`
	ActedBy      *uuid.UUID `json:"acted_by,omitempty"`
	ActedAt      *time.Time `json:"acted_at,omitempty"`
	Comment      string     `json:"comment,omitempty"`
}

type approvalRequestDTO struct {
	ID              uuid.UUID           `json:"id"`
	Module          string              `json:"module"`
	ObjectID        uuid.UUID           `json:"object_id"`
	ObjectReference string              `json:"object_reference"`
	Amount          float64             `json:"amount"`
	Status          string              `json:"status"`
	CurrentSequence int                 `json:"current_sequence"`
	CurrentStep     *approvalActionDTO  `json:"current_step,omitempty"`
	Actions         []approvalActionDTO `json:"actions,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	DecidedAt       *time.Time          `json:"decided_at,omitempty"`
}

func approvalRuleToDTO(ru *ent.ApprovalRule) approvalRuleDTO {
	dto := approvalRuleDTO{
		ID:        ru.ID,
		Module:    string(ru.Module),
		Name:      ru.Name,
		MinAmount: ru.MinAmount,
		MaxAmount: ru.MaxAmount,
		IsActive:  ru.IsActive,
	}
	steps := append([]*ent.ApprovalStep(nil), ru.Edges.Steps...)
	sort.Slice(steps, func(i, j int) bool { return steps[i].Sequence < steps[j].Sequence })
	for _, st := range steps {
		dto.Steps = append(dto.Steps, approvalStepDTO{
			ID: st.ID, Sequence: st.Sequence, Name: st.Name, ApproverRole: st.ApproverRole,
		})
	}
	return dto
}

func approvalActionToDTO(a *ent.ApprovalAction) approvalActionDTO {
	return approvalActionDTO{
		ID: a.ID, Sequence: a.Sequence, Name: a.Name, ApproverRole: a.ApproverRole,
		Status: string(a.Status), ActedBy: a.ActedBy, ActedAt: a.ActedAt, Comment: a.Comment,
	}
}

func approvalRequestToDTO(req *ent.ApprovalRequest) approvalRequestDTO {
	dto := approvalRequestDTO{
		ID: req.ID, Module: string(req.Module), ObjectID: req.ObjectID,
		ObjectReference: req.ObjectReference, Amount: req.Amount, Status: string(req.Status),
		CurrentSequence: req.CurrentSequence, CreatedAt: req.CreatedAt, DecidedAt: req.DecidedAt,
	}
	actions := append([]*ent.ApprovalAction(nil), req.Edges.Actions...)
	sort.Slice(actions, func(i, j int) bool { return actions[i].Sequence < actions[j].Sequence })
	for _, a := range actions {
		ad := approvalActionToDTO(a)
		dto.Actions = append(dto.Actions, ad)
		if a.Sequence == req.CurrentSequence && req.Status == areq.StatusPending {
			cs := ad
			dto.CurrentStep = &cs
		}
	}
	return dto
}

// ─── Identity helpers ────────────────────────────────────────────────────────

// actingUserID returns the authenticated user's UUID from JWT claims.
func actingUserID(r *http.Request) (uuid.UUID, bool) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// approvalRoleCheck builds a closure reporting whether the acting user may approve
// a step requiring a given role. Platform owners / superusers / tenant admins pass
// unconditionally; everyone else must hold the role in this tenant.
func (h *InventoryExtrasHandler) approvalRoleCheck(r *http.Request, tenantID, userID uuid.UUID) func(string) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	isAdmin := ok && (claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsAdmin())
	return func(roleCode string) bool {
		if isAdmin {
			return true
		}
		if h.rbacSvc == nil {
			return false
		}
		has, _ := h.rbacSvc.HasRole(r.Context(), tenantID, userID, roleCode)
		return has
	}
}

// ─── Routes ──────────────────────────────────────────────────────────────────

func (h *InventoryExtrasHandler) registerApprovalRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler) {
	// Rule configuration — GET ungated (read), writes gated by approvals.* perms.
	r.Get("/inventory/approval-rules", h.ListApprovalRules)
	r.Get("/inventory/approval-rules/{ruleID}", h.GetApprovalRule)
	r.With(perm(rbac.PermApprovalsAdd)).Post("/inventory/approval-rules", h.CreateApprovalRule)
	r.With(perm(rbac.PermApprovalsChange)).Put("/inventory/approval-rules/{ruleID}", h.UpdateApprovalRule)
	r.With(perm(rbac.PermApprovalsDelete)).Delete("/inventory/approval-rules/{ruleID}", h.DeleteApprovalRule)

	// Requests — GET ungated; approve/reject are role-gated inside the handler
	// (the step's approver role), with auth enforced upstream.
	r.Get("/inventory/approval-requests", h.ListApprovalRequests)
	r.Get("/inventory/approval-requests/{reqID}", h.GetApprovalRequest)
	r.Post("/inventory/approval-requests/{reqID}/approve", h.ApproveApprovalRequest)
	r.Post("/inventory/approval-requests/{reqID}/reject", h.RejectApprovalRequest)
}

// ─── Rule CRUD ───────────────────────────────────────────────────────────────

type approvalStepPayload struct {
	Sequence     int    `json:"sequence"`
	Name         string `json:"name"`
	ApproverRole string `json:"approver_role"`
}

type approvalRulePayload struct {
	Module    string                `json:"module"`
	Name      string                `json:"name"`
	MinAmount float64               `json:"min_amount"`
	MaxAmount *float64              `json:"max_amount"`
	IsActive  *bool                 `json:"is_active"`
	Steps     []approvalStepPayload `json:"steps"`
}

// validApprovalModule accepts every module the ApprovalRule schema supports —
// procurement, stock movements, manufacturing, assets, RFQs/contracts and stock
// counts alike. (It used to whitelist only purchase_order/requisition, so rules
// for any other module 400'd even though the engine + schema fully support them.)
func validApprovalModule(m string) bool {
	return arule.ModuleValidator(arule.Module(m)) == nil
}

// ListApprovalRules handles GET /inventory/approval-rules.
//
//	@Summary  List approval rules
//	@Tags     Approvals
//	@Produce  json
//	@Param    module  query  string  false  "Filter by module"
//	@Success  200  {array}  approvalRuleDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/approval-rules [get]
func (h *InventoryExtrasHandler) ListApprovalRules(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.ApprovalRule.Query().Where(arule.TenantID(tenantID)).WithSteps()
	if m := r.URL.Query().Get("module"); m != "" {
		q = q.Where(arule.ModuleEQ(arule.Module(m)))
	}
	rules, err := q.Order(ent.Desc(arule.FieldCreatedAt)).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list approval rules")
		return
	}
	out := make([]approvalRuleDTO, 0, len(rules))
	for _, ru := range rules {
		out = append(out, approvalRuleToDTO(ru))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetApprovalRule handles GET /inventory/approval-rules/{ruleID}.
func (h *InventoryExtrasHandler) GetApprovalRule(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "ruleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid rule ID")
		return
	}
	ru, err := h.orm.ApprovalRule.Query().Where(arule.ID(id), arule.TenantID(tenantID)).WithSteps().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Approval rule not found")
		return
	}
	writeJSON(w, http.StatusOK, approvalRuleToDTO(ru))
}

// CreateApprovalRule handles POST /inventory/approval-rules.
//
//	@Summary  Create an approval rule with steps
//	@Tags     Approvals
//	@Accept   json
//	@Produce  json
//	@Param    body  body  approvalRulePayload  true  "Rule payload"
//	@Success  201  {object}  approvalRuleDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/approval-rules [post]
func (h *InventoryExtrasHandler) CreateApprovalRule(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var p approvalRulePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if !validApprovalModule(p.Module) {
		writeError(w, http.StatusBadRequest, "INVALID_MODULE", "Unknown approval module — use one of the document types listed in the rule builder (purchase_order, requisition, stock_transfer, purchase_return, goods_receipt, production_batch, asset_disposal, asset_transfer, asset_maintenance, rfq, contract, stock_adjustment, stock_writeoff, stock_count)")
		return
	}
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	if len(p.Steps) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_STEPS", "at least one approval step is required")
		return
	}
	create := h.orm.ApprovalRule.Create().
		SetTenantID(tenantID).
		SetModule(arule.Module(p.Module)).
		SetName(p.Name).
		SetMinAmount(p.MinAmount)
	if p.MaxAmount != nil {
		create = create.SetMaxAmount(*p.MaxAmount)
	}
	if p.IsActive != nil {
		create = create.SetIsActive(*p.IsActive)
	}
	ru, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create approval rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create approval rule")
		return
	}
	if err := h.createApprovalSteps(r, tenantID, ru.ID, p.Steps); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create approval steps")
		return
	}
	full, _ := h.orm.ApprovalRule.Query().Where(arule.ID(ru.ID)).WithSteps().Only(r.Context())
	writeJSON(w, http.StatusCreated, approvalRuleToDTO(full))
}

// createApprovalSteps writes the ordered steps for a rule, normalizing sequences to 1..N.
func (h *InventoryExtrasHandler) createApprovalSteps(r *http.Request, tenantID, ruleID uuid.UUID, steps []approvalStepPayload) error {
	ordered := append([]approvalStepPayload(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	for i, st := range ordered {
		name := st.Name
		if name == "" {
			name = "Step"
		}
		role := st.ApproverRole
		if role == "" {
			role = rbac.RoleInventoryAdmin
		}
		if _, err := h.orm.ApprovalStep.Create().
			SetTenantID(tenantID).
			SetRuleID(ruleID).
			SetSequence(i + 1).
			SetName(name).
			SetApproverRole(role).
			Save(r.Context()); err != nil {
			h.log.Error("create approval step failed", zap.Error(err))
			return err
		}
	}
	return nil
}

// UpdateApprovalRule handles PUT /inventory/approval-rules/{ruleID}. Replaces steps.
func (h *InventoryExtrasHandler) UpdateApprovalRule(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "ruleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid rule ID")
		return
	}
	ru, err := h.orm.ApprovalRule.Query().Where(arule.ID(id), arule.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Approval rule not found")
		return
	}
	var p approvalRulePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	upd := h.orm.ApprovalRule.UpdateOneID(ru.ID)
	if p.Name != "" {
		upd = upd.SetName(p.Name)
	}
	if validApprovalModule(p.Module) {
		upd = upd.SetModule(arule.Module(p.Module))
	}
	upd = upd.SetMinAmount(p.MinAmount)
	if p.MaxAmount != nil {
		upd = upd.SetMaxAmount(*p.MaxAmount)
	} else {
		upd = upd.ClearMaxAmount()
	}
	if p.IsActive != nil {
		upd = upd.SetIsActive(*p.IsActive)
	}
	if _, err := upd.Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update approval rule")
		return
	}
	// Replace steps when provided.
	if p.Steps != nil {
		if _, err := h.orm.ApprovalStep.Delete().Where(astep.RuleID(ru.ID)).Exec(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to clear steps")
			return
		}
		if err := h.createApprovalSteps(r, tenantID, ru.ID, p.Steps); err != nil {
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to write steps")
			return
		}
	}
	full, _ := h.orm.ApprovalRule.Query().Where(arule.ID(ru.ID)).WithSteps().Only(r.Context())
	writeJSON(w, http.StatusOK, approvalRuleToDTO(full))
}

// DeleteApprovalRule handles DELETE /inventory/approval-rules/{ruleID}.
func (h *InventoryExtrasHandler) DeleteApprovalRule(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "ruleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid rule ID")
		return
	}
	ru, err := h.orm.ApprovalRule.Query().Where(arule.ID(id), arule.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Approval rule not found")
		return
	}
	_, _ = h.orm.ApprovalStep.Delete().Where(astep.RuleID(ru.ID)).Exec(r.Context())
	if err := h.orm.ApprovalRule.DeleteOneID(ru.ID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete approval rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": ru.ID})
}

// ─── Requests ────────────────────────────────────────────────────────────────

// ListApprovalRequests handles GET /inventory/approval-requests.
// Filters: status, module, object_id, inbox=true (pending steps the caller can act on).
//
//	@Summary  List approval requests
//	@Tags     Approvals
//	@Produce  json
//	@Param    status     query  string  false  "Filter by status"
//	@Param    module     query  string  false  "Filter by module"
//	@Param    object_id  query  string  false  "Filter by document ID"
//	@Param    inbox      query  bool    false  "Only requests awaiting the caller"
//	@Success  200  {array}  approvalRequestDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/approval-requests [get]
func (h *InventoryExtrasHandler) ListApprovalRequests(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.ApprovalRequest.Query().Where(areq.TenantID(tenantID)).WithActions()
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(areq.StatusEQ(areq.Status(s)))
	}
	if m := r.URL.Query().Get("module"); m != "" {
		q = q.Where(areq.ModuleEQ(areq.Module(m)))
	}
	if oid := r.URL.Query().Get("object_id"); oid != "" {
		if id, e := uuid.Parse(oid); e == nil {
			q = q.Where(areq.ObjectID(id))
		}
	}
	inbox := r.URL.Query().Get("inbox") == "true"
	if inbox {
		q = q.Where(areq.StatusEQ(areq.StatusPending))
	}
	rows, err := q.Order(ent.Desc(areq.FieldCreatedAt)).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list approval requests")
		return
	}

	// For the inbox view, keep only requests whose current step the caller may approve.
	var roleSet map[string]bool
	isAdmin := false
	if inbox {
		if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
			isAdmin = claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsAdmin()
		}
		if userID, ok := actingUserID(r); ok && h.rbacSvc != nil {
			roleSet = map[string]bool{}
			if roles, e := h.rbacSvc.GetUserRoles(r.Context(), tenantID, userID); e == nil {
				for _, ro := range roles {
					roleSet[ro.RoleCode] = true
				}
			}
		}
	}

	out := make([]approvalRequestDTO, 0, len(rows))
	for _, req := range rows {
		dto := approvalRequestToDTO(req)
		if inbox && !isAdmin {
			if dto.CurrentStep == nil || roleSet == nil || !roleSet[dto.CurrentStep.ApproverRole] {
				continue
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetApprovalRequest handles GET /inventory/approval-requests/{reqID}.
func (h *InventoryExtrasHandler) GetApprovalRequest(w http.ResponseWriter, r *http.Request) {
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
	req, err := h.orm.ApprovalRequest.Query().Where(areq.ID(id), areq.TenantID(tenantID)).WithActions().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Approval request not found")
		return
	}
	writeJSON(w, http.StatusOK, approvalRequestToDTO(req))
}

// ApproveApprovalRequest handles POST /inventory/approval-requests/{reqID}/approve.
func (h *InventoryExtrasHandler) ApproveApprovalRequest(w http.ResponseWriter, r *http.Request) {
	h.actOnApprovalRequest(w, r, approvals.DecisionApprove)
}

// RejectApprovalRequest handles POST /inventory/approval-requests/{reqID}/reject.
func (h *InventoryExtrasHandler) RejectApprovalRequest(w http.ResponseWriter, r *http.Request) {
	h.actOnApprovalRequest(w, r, approvals.DecisionReject)
}

func (h *InventoryExtrasHandler) actOnApprovalRequest(w http.ResponseWriter, r *http.Request, decision approvals.Decision) {
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
	var body struct {
		Comment string `json:"comment"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.runApprovalAction(w, r, tenantID, id, decision, body.Comment)
}

// applyApprovalDecision runs a decision through the engine, syncs the underlying
// document, and emits an event. On failure it writes the HTTP error and returns
// ok=false; on success it returns the updated request and leaves response
// rendering to the caller. Shared by the generic and requisition approve/reject
// endpoints so each can render its own DTO shape.
func (h *InventoryExtrasHandler) applyApprovalDecision(w http.ResponseWriter, r *http.Request, tenantID, requestID uuid.UUID, decision approvals.Decision, comment string) (*ent.ApprovalRequest, bool) {
	userID, ok := actingUserID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return nil, false
	}
	check := h.approvalRoleCheck(r, tenantID, userID)
	updated, err := h.approvals().Act(r.Context(), tenantID, requestID, userID, decision, comment, check)
	if err != nil {
		switch {
		case errors.Is(err, approvals.ErrForbiddenRole):
			writeError(w, http.StatusForbidden, "FORBIDDEN_ROLE", "You do not hold the role required to approve this step")
		case errors.Is(err, approvals.ErrNotPending):
			writeError(w, http.StatusConflict, "NOT_PENDING", "Approval request is not pending")
		case errors.Is(err, approvals.ErrNoCurrentStep):
			writeError(w, http.StatusConflict, "NO_STEP", "No pending step to act on")
		default:
			writeError(w, http.StatusInternalServerError, "ACT_FAILED", "Failed to record decision")
		}
		return nil, false
	}
	h.syncDocFromApproval(r, updated)
	h.publishOutbox(r.Context(), tenantID, "approval_request", updated.ID, "inventory.approval."+string(updated.Status), map[string]any{
		"id": updated.ID, "module": updated.Module, "object_id": updated.ObjectID, "status": updated.Status,
	})
	return updated, true
}

// runApprovalAction applies a decision and writes the updated request DTO (used by
// the generic /approval-requests/{id}/approve|reject endpoints).
func (h *InventoryExtrasHandler) runApprovalAction(w http.ResponseWriter, r *http.Request, tenantID, requestID uuid.UUID, decision approvals.Decision, comment string) {
	updated, ok := h.applyApprovalDecision(w, r, tenantID, requestID, decision, comment)
	if !ok {
		return
	}
	full, _ := h.orm.ApprovalRequest.Query().Where(areq.ID(updated.ID)).WithActions().Only(r.Context())
	writeJSON(w, http.StatusOK, approvalRequestToDTO(full))
}

// syncDocFromApproval mirrors a terminal approval decision onto the underlying
// document. Requisitions transition to approved/rejected; purchase orders rely on
// the send-time gate so need no status change here.
func (h *InventoryExtrasHandler) syncDocFromApproval(r *http.Request, req *ent.ApprovalRequest) {
	if req.Module != areq.ModuleRequisition {
		return
	}
	var st entreq.Status
	switch req.Status {
	case areq.StatusApproved:
		st = entreq.StatusApproved
	case areq.StatusRejected:
		st = entreq.StatusRejected
	default:
		return
	}
	if _, err := h.orm.Requisition.UpdateOneID(req.ObjectID).SetStatus(st).Save(r.Context()); err != nil {
		h.log.Warn("sync requisition from approval failed", zap.Error(err))
		return
	}
	h.publishOutbox(r.Context(), req.TenantID, "requisition", req.ObjectID, "inventory.requisition."+string(st), map[string]any{
		"id": req.ObjectID, "status": st, "via": "approval_matrix",
	})
	// A matrix-approved requisition auto-raises its purchase order(s), same as the
	// direct-approve path — so multi-step sign-off ends in procurement too.
	if st == entreq.StatusApproved {
		if rq, err := h.orm.Requisition.Get(r.Context(), req.ObjectID); err == nil {
			h.autoCreatePOsFromRequisition(r.Context(), req.TenantID, rq)
		}
	}
}

// ─── Submit-for-approval (purchase orders) ───────────────────────────────────

// SubmitPurchaseOrderForApproval handles POST /inventory/purchase-orders/{poID}/submit-for-approval.
// Creates an approval request if a rule matches the PO amount; otherwise reports
// that no approval is required and the PO may be sent directly.
//
//	@Summary  Submit a purchase order for approval
//	@Tags     Approvals
//	@Produce  json
//	@Param    poID  path  string  true  "Purchase order ID"
//	@Success  200  {object}  map[string]interface{}
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/purchase-orders/{poID}/submit-for-approval [post]
func (h *InventoryExtrasHandler) SubmitPurchaseOrderForApproval(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.orm.PurchaseOrder.Query().Where(entpo.ID(poID), entpo.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() != "draft" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft purchase orders can be submitted for approval")
		return
	}
	var submittedBy *uuid.UUID
	if uid, ok := actingUserID(r); ok {
		submittedBy = &uid
	}
	req, required, err := h.approvals().Submit(r.Context(), tenantID, string(arule.ModulePurchaseOrder), po.ID, po.PoNumber, po.TotalAmount, submittedBy)
	if err != nil {
		h.log.Error("submit PO for approval failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "SUBMIT_FAILED", "Failed to submit for approval")
		return
	}
	if !required {
		writeJSON(w, http.StatusOK, map[string]any{"approval_required": false, "message": "No approval rule matches this amount; the order can be sent directly."})
		return
	}
	h.publishOutbox(r.Context(), tenantID, "approval_request", req.ID, "inventory.approval.submitted", map[string]any{
		"id": req.ID, "module": "purchase_order", "object_id": po.ID, "amount": po.TotalAmount,
	})
	full, _ := h.orm.ApprovalRequest.Query().Where(areq.ID(req.ID)).WithActions().Only(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"approval_required": true, "request": approvalRequestToDTO(full)})
}

// gateApproval enforces the approval matrix on ANY effecting action across the
// inventory service (procurement, manufacturing, assets, stock). It returns true
// when the caller may proceed — no matching rule (not_required) or an approved
// request exists. Otherwise it writes a 409 and returns false: when no request
// exists yet it auto-creates one ("submitted for approval"); a pending/rejected
// request reports that state. Module-agnostic so every workflow reuses one path;
// mirrors the PO-send gate. Amount 0 means "gate whenever any rule exists".
func (h *InventoryExtrasHandler) gateApproval(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, module string, objectID uuid.UUID, reference string, amount float64) bool {
	ok, state, err := h.approvals().Satisfied(r.Context(), tenantID, module, objectID, amount)
	if err != nil {
		// Fail open on engine errors — never block a legitimate action on infra failure.
		h.log.Warn("approval gate check failed; allowing", zap.String("module", module), zap.Error(err))
		return true
	}
	if ok {
		return true // not_required or approved
	}
	switch state {
	case "not_submitted":
		var submittedBy *uuid.UUID
		if uid, ok := actingUserID(r); ok {
			submittedBy = &uid
		}
		if req, required, serr := h.approvals().Submit(r.Context(), tenantID, module, objectID, reference, amount, submittedBy); serr == nil && required {
			h.publishOutbox(r.Context(), tenantID, "approval_request", req.ID, "inventory.approval.submitted", map[string]any{
				"id": req.ID, "module": module, "object_id": objectID, "amount": amount,
			})
		}
		writeError(w, http.StatusConflict, "APPROVAL_REQUIRED", "This action requires approval — it has been submitted for sign-off.")
	case "pending":
		writeError(w, http.StatusConflict, "APPROVAL_PENDING", "This action is awaiting approval sign-off.")
	case "rejected":
		writeError(w, http.StatusConflict, "APPROVAL_REJECTED", "The approval request for this action was rejected.")
	default:
		writeError(w, http.StatusConflict, "APPROVAL_REQUIRED", "This action requires approval before it can proceed.")
	}
	return false
}
