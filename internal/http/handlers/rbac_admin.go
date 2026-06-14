package handlers

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// canManageRBAC reports whether the caller may create/edit roles and manage
// role permissions (inventory_admin or warehouse_manager). Writes the error
// response and returns false when not permitted.
func (h *RBACHandler) canManageRBAC(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	uid, err := claims.UserID()
	if err != nil || uid == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid user ID"})
		return false
	}
	for _, role := range []string{rbac.RoleInventoryAdmin, rbac.RoleWarehouseManager} {
		if has, _ := h.rbacService.HasRole(r.Context(), tenantID, uid, role); has {
			return true
		}
	}
	respondJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
	return false
}

// createRoleRequest is the body for creating a custom tenant role.
type createRoleRequest struct {
	RoleCode    string `json:"role_code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateRole handles POST /rbac/roles — creates a tenant-scoped custom role.
func (h *RBACHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}
	if !h.canManageRBAC(w, r, tenantID) {
		return
	}
	var req createRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleCode == "" || req.Name == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "role_code and name are required"})
		return
	}
	role := &rbac.InventoryRole{
		ID:           uuid.New(),
		TenantID:     &tenantID,
		RoleCode:     req.RoleCode,
		Name:         req.Name,
		IsSystemRole: false,
	}
	if req.Description != "" {
		role.Description = &req.Description
	}
	if err := h.rbacRepo.CreateRole(r.Context(), tenantID, role); err != nil {
		h.logger.Error("create role failed", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create role"})
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"id": role.ID, "role_code": role.RoleCode, "name": role.Name})
}

// GetRolePermissions handles GET /rbac/roles/{roleID}/permissions.
func (h *RBACHandler) GetRolePermissions(w http.ResponseWriter, r *http.Request) {
	roleID, err := uuid.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid role ID"})
		return
	}
	perms, err := h.rbacRepo.GetRolePermissions(r.Context(), roleID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load role permissions"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"permissions": perms})
}

// setRolePermissionsRequest replaces a role's permission set.
type setRolePermissionsRequest struct {
	PermissionIDs []uuid.UUID `json:"permission_ids"`
}

// SetRolePermissions handles PUT /rbac/roles/{roleID}/permissions — replaces
// the role's permissions with the supplied set (the permission-matrix save).
func (h *RBACHandler) SetRolePermissions(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}
	if !h.canManageRBAC(w, r, tenantID) {
		return
	}
	roleID, err := uuid.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid role ID"})
		return
	}
	var req setRolePermissionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	ctx := r.Context()

	current, err := h.rbacRepo.GetRolePermissions(ctx, roleID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load role permissions"})
		return
	}
	have := make(map[uuid.UUID]bool, len(current))
	for _, p := range current {
		have[p.ID] = true
	}
	want := make(map[uuid.UUID]bool, len(req.PermissionIDs))
	for _, id := range req.PermissionIDs {
		want[id] = true
	}
	// Add newly-selected permissions.
	for id := range want {
		if !have[id] {
			if err := h.rbacRepo.AssignPermissionToRole(ctx, roleID, id); err != nil {
				h.logger.Warn("assign permission to role failed", zap.String("permission", id.String()), zap.Error(err))
			}
		}
	}
	// Remove de-selected permissions.
	for id := range have {
		if !want[id] {
			if err := h.rbacRepo.RemovePermissionFromRole(ctx, roleID, id); err != nil {
				h.logger.Warn("remove permission from role failed", zap.String("permission", id.String()), zap.Error(err))
			}
		}
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "role permissions updated"})
}

// updateUserStatusRequest activates/deactivates a user.
type updateUserStatusRequest struct {
	Status string `json:"status"` // active | inactive | suspended
}

// UpdateUserStatus handles PUT /users/{userID}/status.
func (h *RBACHandler) UpdateUserStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}
	if !h.canManageRBAC(w, r, tenantID) {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user ID"})
		return
	}
	var req updateUserStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}
	switch req.Status {
	case "active", "inactive", "suspended":
	default:
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}
	if err := h.rbacRepo.UpdateUser(r.Context(), tenantID, userID, &rbac.UserUpdates{Status: &req.Status}); err != nil {
		h.logger.Error("update user status failed", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update user"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "user updated"})
}
