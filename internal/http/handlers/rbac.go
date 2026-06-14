package handlers

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/services/usersync"
)

// RBACHandler handles RBAC-related operations.
type RBACHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
	syncService *usersync.Service
	rbacRepo    rbac.Repository
}

// NewRBACHandler creates a new RBAC handler.
func NewRBACHandler(logger *zap.Logger, rbacService *rbac.Service, syncService *usersync.Service, rbacRepo rbac.Repository) *RBACHandler {
	return &RBACHandler{
		logger:      logger,
		rbacService: rbacService,
		syncService: syncService,
		rbacRepo:    rbacRepo,
	}
}

// AssignRoleRequest represents a request to assign a role.
type AssignRoleRequest struct {
	UserID uuid.UUID `json:"user_id"`
	RoleID uuid.UUID `json:"role_id"`
}

// AssignRole assigns a role to a user.
func (h *RBACHandler) AssignRole(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	var req AssignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	assignedBy, err := claims.UserID()
	if err != nil || assignedBy == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid user ID"})
		return
	}

	// Only users with inventory_admin or warehouse_manager role may assign roles.
	canAssign := false
	for _, allowedRole := range []string{"inventory_admin", "warehouse_manager"} {
		if ok, _ := h.rbacService.HasRole(r.Context(), tenantID, assignedBy, allowedRole); ok {
			canAssign = true
			break
		}
	}
	if !canAssign {
		respondJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions to assign roles"})
		return
	}

	if err := h.rbacService.AssignRole(r.Context(), tenantID, req.UserID, req.RoleID, assignedBy); err != nil {
		h.logger.Error("failed to assign role", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to assign role"})
		return
	}

	respondJSON(w, http.StatusCreated, map[string]string{"message": "role assigned successfully"})
}

// RevokeRole revokes a role from a user.
func (h *RBACHandler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	assignmentIDStr := chi.URLParam(r, "id")
	assignmentID, err := uuid.Parse(assignmentIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid assignment ID"})
		return
	}

	// Get assignment to extract user ID and role ID
	assignments, err := h.rbacRepo.ListUserAssignments(r.Context(), tenantID, rbac.AssignmentFilters{})
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get assignment"})
		return
	}

	var assignment *rbac.UserRoleAssignment
	for _, a := range assignments {
		if a.ID == assignmentID {
			assignment = a
			break
		}
	}

	if assignment == nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "assignment not found"})
		return
	}

	if err := h.rbacService.RevokeRole(r.Context(), tenantID, assignment.UserID, assignment.RoleID); err != nil {
		h.logger.Error("failed to revoke role", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke role"})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"message": "role revoked successfully"})
}

// ListAssignments lists all role assignments.
func (h *RBACHandler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	assignments, err := h.rbacRepo.ListUserAssignments(r.Context(), tenantID, rbac.AssignmentFilters{})
	if err != nil {
		h.logger.Error("failed to list assignments", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list assignments"})
		return
	}

	out := make([]assignmentDTO, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, assignmentDTO{ID: a.ID, UserID: a.UserID, RoleID: a.RoleID, AssignedAt: a.AssignedAt})
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"data": out, "assignments": out})
}

// ListRoles lists all roles.
func (h *RBACHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant")
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant ID"})
		return
	}

	roles, err := h.rbacRepo.ListRoles(r.Context(), tenantID)
	if err != nil {
		h.logger.Error("failed to list roles", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list roles"})
		return
	}

	out := make([]roleDTO, 0, len(roles))
	for _, ro := range roles {
		out = append(out, toRoleDTO(ro))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"data": out, "roles": out})
}

// ListPermissions lists all permissions.
func (h *RBACHandler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	permissions, err := h.rbacRepo.ListPermissions(r.Context(), rbac.PermissionFilters{})
	if err != nil {
		h.logger.Error("failed to list permissions", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list permissions"})
		return
	}

	out := make([]permDTO, 0, len(permissions))
	for _, p := range permissions {
		out = append(out, toPermDTO(p))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"data": out, "permissions": out})
}

// GetMyPermissions returns the permissions for the current user.
func (h *RBACHandler) GetMyPermissions(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	userID, err := claims.UserID()
	if err != nil || userID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "user ID required"})
		return
	}

	tenantID, err := claims.TenantUUID()
	if err != nil || tenantID == nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant ID required"})
		return
	}

	permissions, err := h.rbacService.GetUserPermissions(r.Context(), *tenantID, userID)
	if err != nil {
		h.logger.Error("failed to get user permissions", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get permissions"})
		return
	}

	out := make([]permDTO, 0, len(permissions))
	for _, p := range permissions {
		out = append(out, toPermDTO(p))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"data": out, "permissions": out})
}

// GetMyRoles returns the roles for the current user.
func (h *RBACHandler) GetMyRoles(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	userID, err := claims.UserID()
	if err != nil || userID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "user ID required"})
		return
	}

	tenantID, err := claims.TenantUUID()
	if err != nil || tenantID == nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant ID required"})
		return
	}

	roles, err := h.rbacService.GetUserRoles(r.Context(), *tenantID, userID)
	if err != nil {
		h.logger.Error("failed to get user roles", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get roles"})
		return
	}

	out := make([]roleDTO, 0, len(roles))
	for _, ro := range roles {
		out = append(out, toRoleDTO(ro))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"data": out, "roles": out})
}

// RegisterRBACRoutes registers RBAC routes.
func (h *RBACHandler) RegisterRBACRoutes(r chi.Router) {
	r.Post("/rbac/assignments", h.AssignRole)
	r.Get("/rbac/assignments", h.ListAssignments)
	r.Delete("/rbac/assignments/{id}", h.RevokeRole)
	r.Get("/rbac/roles", h.ListRoles)
	r.Post("/rbac/roles", h.CreateRole)
	r.Get("/rbac/roles/{roleID}/permissions", h.GetRolePermissions)
	r.Put("/rbac/roles/{roleID}/permissions", h.SetRolePermissions)
	r.Get("/rbac/permissions", h.ListPermissions)
	r.Put("/users/{userID}/status", h.UpdateUserStatus)
	r.Get("/users/me/permissions", h.GetMyPermissions)
	r.Get("/users/me/roles", h.GetMyRoles)
}
