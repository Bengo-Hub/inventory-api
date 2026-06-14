package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entauditlog "github.com/bengobox/inventory-service/internal/ent/auditlog"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// RegisterPrivateRoutes wires user-identity routes that always require auth
// (registered under the private group, not the public-GET inventory group):
// the current user's assigned outlets, user-outlet assignment management, and
// the centralized audit-log read API.
func (h *WarehouseHandler) RegisterPrivateRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	// Outlets the current user may operate in (assignment-filtered).
	r.Get("/inventory/my-outlets", h.MyOutlets)

	// User-outlet assignment management (admin only).
	r.With(perm(rbac.PermUsersManage)).Get("/inventory/users/{userID}/outlets", h.ListUserOutlets)
	r.With(perm(rbac.PermUsersManage)).Post("/inventory/users/{userID}/outlets", h.AssignUserOutlet)
	r.With(perm(rbac.PermUsersManage)).Delete("/inventory/users/{userID}/outlets/{outletID}", h.RemoveUserOutlet)

	// Centralized audit trail (read-only).
	r.With(perm(rbac.PermAuditView)).Get("/inventory/audit-logs", h.ListAuditLogs)
}

// outletItem mirrors the auth-api outlet shape the UI consumes.
type outletItem struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	UseCase string `json:"use_case,omitempty"`
	IsHQ    bool   `json:"is_hq,omitempty"`
	Status  string `json:"status,omitempty"`
}

// fetchTenantOutlets calls auth-api and returns the parsed outlet list for a tenant slug.
func (h *WarehouseHandler) fetchTenantOutlets(r *http.Request, slug string) ([]outletItem, error) {
	if h.authURL == "" {
		return nil, fmt.Errorf("auth service URL not configured")
	}
	url := fmt.Sprintf("%s/api/v1/tenants/%s/outlets", h.authURL, slug)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := s2sHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("auth-api returned %d", resp.StatusCode)
	}
	// auth-api may return either a bare array or a {"data":[...]} envelope.
	var env struct {
		Data []outletItem `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Data != nil {
		return env.Data, nil
	}
	var arr []outletItem
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

// MyOutlets handles GET /inventory/my-outlets — returns the outlets the current
// user is assigned to. HQ / all-outlet users receive the full tenant outlet list.
func (h *WarehouseHandler) MyOutlets(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	slug := chi.URLParam(r, "tenant")
	if _, e := uuid.Parse(slug); e == nil {
		// URL param was a UUID; fall back to claims slug for the auth-api call.
		slug = claims.GetTenantSlug()
	}

	all, err := h.fetchTenantOutlets(r, slug)
	if err != nil {
		h.log.Error("my-outlets: fetch tenant outlets failed", zap.Error(err))
		writeError(w, http.StatusBadGateway, "PROXY_ERROR", "Failed to fetch outlets")
		return
	}

	// HQ / platform owners see everything.
	if claims.CanAccessAllOutlets() {
		writeJSON(w, http.StatusOK, map[string]any{"data": all, "is_hq": true})
		return
	}

	userID, _ := claims.UserID()
	assigned, err := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(r.Context())
	if err != nil {
		h.log.Error("my-outlets: query assignments failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load assignments")
		return
	}
	allowed := make(map[string]bool, len(assigned))
	for _, a := range assigned {
		allowed[a.OutletID.String()] = true
	}
	filtered := make([]outletItem, 0, len(assigned))
	for _, o := range all {
		if allowed[o.ID] {
			filtered = append(filtered, o)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": filtered, "is_hq": false})
}

// userOutletDTO is one assigned outlet in responses.
type userOutletDTO struct {
	ID           uuid.UUID `json:"id"`
	OutletID     uuid.UUID `json:"outlet_id"`
	IsHomeOutlet bool      `json:"is_home_outlet"`
	AssignedAt   time.Time `json:"assigned_at"`
}

// ListUserOutlets handles GET /inventory/users/{userID}/outlets.
func (h *WarehouseHandler) ListUserOutlets(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER", "Invalid user ID")
		return
	}
	rows, err := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load assignments")
		return
	}
	out := make([]userOutletDTO, 0, len(rows))
	for _, a := range rows {
		out = append(out, userOutletDTO{ID: a.ID, OutletID: a.OutletID, IsHomeOutlet: a.IsHomeOutlet, AssignedAt: a.AssignedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// assignOutletRequest assigns one outlet to a user.
type assignOutletRequest struct {
	OutletID     uuid.UUID `json:"outlet_id"`
	IsHomeOutlet bool      `json:"is_home_outlet"`
}

// AssignUserOutlet handles POST /inventory/users/{userID}/outlets.
func (h *WarehouseHandler) AssignUserOutlet(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER", "Invalid user ID")
		return
	}
	var req assignOutletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OutletID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "outlet_id is required")
		return
	}
	ctx := r.Context()

	// Idempotent: skip if already assigned.
	exists, _ := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID), entuseroutlet.OutletID(req.OutletID)).
		Exist(ctx)
	if !exists {
		b := h.orm.UserOutlet.Create().
			SetTenantID(tenantID).
			SetUserID(userID).
			SetOutletID(req.OutletID).
			SetIsHomeOutlet(req.IsHomeOutlet)
		if claims, ok := authclient.ClaimsFromContext(ctx); ok && claims != nil {
			if actor, e := claims.UserID(); e == nil {
				b = b.SetAssignedBy(actor)
			}
		}
		if _, err := b.Save(ctx); err != nil {
			h.log.Error("assign user outlet failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to assign outlet")
			return
		}
		h.recordOutletAssignmentAudit(ctx, tenantID, userID, req.OutletID, "user.outlet_assign")
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "assigned"})
}

// RemoveUserOutlet handles DELETE /inventory/users/{userID}/outlets/{outletID}.
func (h *WarehouseHandler) RemoveUserOutlet(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER", "Invalid user ID")
		return
	}
	outletID, err := uuid.Parse(chi.URLParam(r, "outletID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_OUTLET", "Invalid outlet ID")
		return
	}
	ctx := r.Context()
	n, err := h.orm.UserOutlet.Delete().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID), entuseroutlet.OutletID(outletID)).
		Exec(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to remove assignment")
		return
	}
	if n > 0 {
		h.recordOutletAssignmentAudit(ctx, tenantID, userID, outletID, "user.outlet_unassign")
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

func (h *WarehouseHandler) recordOutletAssignmentAudit(ctx context.Context, tenantID, userID, outletID uuid.UUID, action string) {
	if h.auditSvc == nil {
		return
	}
	var actor uuid.UUID
	if claims, ok := authclient.ClaimsFromContext(ctx); ok && claims != nil {
		actor, _ = claims.UserID()
	}
	oid := outletID
	h.auditSvc.Record(ctx, audit.Entry{
		TenantID:    tenantID,
		OutletID:    &oid,
		ActorUserID: actor,
		Action:      action,
		EntityType:  "user_outlet",
		EntityID:    userID.String(),
		After:       map[string]any{"user_id": userID.String(), "outlet_id": outletID.String()},
	})
}

// auditLogDTO is one audit entry in list responses.
type auditLogDTO struct {
	ID         uuid.UUID      `json:"id"`
	OutletID   *uuid.UUID     `json:"outlet_id,omitempty"`
	Actor      uuid.UUID      `json:"actor_user_id"`
	Approver   *uuid.UUID     `json:"approver_user_id,omitempty"`
	Action     string         `json:"action"`
	EntityType string         `json:"entity_type,omitempty"`
	EntityID   string         `json:"entity_id,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Before     map[string]any `json:"before_json,omitempty"`
	After      map[string]any `json:"after_json,omitempty"`
	Amount     *float64       `json:"amount,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// ListAuditLogs handles GET /inventory/audit-logs with optional filters:
// action, actor, outlet, from, to, limit, offset. Returns a {data,total} envelope.
func (h *WarehouseHandler) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.AuditLog.Query().Where(entauditlog.TenantID(tenantID))
	if a := r.URL.Query().Get("action"); a != "" {
		q = q.Where(entauditlog.Action(a))
	}
	if a := r.URL.Query().Get("actor"); a != "" {
		if id, e := uuid.Parse(a); e == nil {
			q = q.Where(entauditlog.ActorUserID(id))
		}
	}
	if o := r.URL.Query().Get("outlet"); o != "" {
		if id, e := uuid.Parse(o); e == nil {
			q = q.Where(entauditlog.OutletID(id))
		}
	}
	if f := r.URL.Query().Get("from"); f != "" {
		if t, e := time.Parse(time.RFC3339, f); e == nil {
			q = q.Where(entauditlog.CreatedAtGTE(t))
		}
	}
	if t := r.URL.Query().Get("to"); t != "" {
		if tt, e := time.Parse(time.RFC3339, t); e == nil {
			q = q.Where(entauditlog.CreatedAtLTE(tt))
		}
	}
	total, _ := q.Clone().Count(r.Context())
	limit := parseInt(r.URL.Query().Get("limit"), 50)
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	rows, err := q.Order(ent.Desc(entauditlog.FieldCreatedAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load audit logs")
		return
	}
	out := make([]auditLogDTO, 0, len(rows))
	for _, a := range rows {
		d := auditLogDTO{
			ID: a.ID, Actor: a.ActorUserID, Action: a.Action,
			EntityType: a.EntityType, EntityID: a.EntityID, Reason: a.Reason,
			Before: a.BeforeJSON, After: a.AfterJSON, CreatedAt: a.CreatedAt,
		}
		d.OutletID = a.OutletID
		d.Approver = a.ApproverUserID
		d.Amount = a.Amount
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "total": total})
}
