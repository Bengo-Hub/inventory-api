package handlers

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// AuthHandler handles service-level auth sync for inventory-ui.
type AuthHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(logger *zap.Logger, rbacService *rbac.Service) *AuthHandler {
	return &AuthHandler{
		logger:      logger.Named("auth.Handler"),
		rbacService: rbacService,
	}
}

// MeResponse is the payload returned by GET /auth/me.
type MeResponse struct {
	ID              string   `json:"id"`
	Email           string   `json:"email"`
	TenantID        string   `json:"tenant_id"`
	TenantSlug      string   `json:"tenant_slug"`
	Roles           []string `json:"roles"`
	Permissions     []string `json:"permissions"`
	IsPlatformOwner bool     `json:"is_platform_owner"`
	IsSuperUser     bool     `json:"is_superuser"`
}

// Me handles GET /auth/me.
// Called by inventory-ui after SSO callback to sync local RBAC roles and permissions.
// JWT is already validated by auth middleware; user is JIT-provisioned by the router middleware.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing claims")
		return
	}

	userID, err := claims.UserID()
	if err != nil || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid user ID in token")
		return
	}

	tenantID, err := claims.TenantUUID()
	if err != nil || tenantID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid tenant ID in token")
		return
	}

	// Base roles/permissions come from JWT claims (always present immediately after SSO login).
	roles := make([]string, len(claims.Roles))
	copy(roles, claims.Roles)

	permissions := make([]string, len(claims.Permissions))
	copy(permissions, claims.Permissions)

	// Augment with service-level RBAC assignments stored in inventory DB.
	if h.rbacService != nil {
		if svcRoles, rErr := h.rbacService.GetUserRoles(r.Context(), *tenantID, userID); rErr == nil {
			for _, sr := range svcRoles {
				roles = appendUniqueStr(roles, sr.RoleCode)
			}
		} else {
			h.logger.Debug("auth/me: failed to get local roles", zap.Error(rErr))
		}

		if svcPerms, pErr := h.rbacService.GetUserPermissions(r.Context(), *tenantID, userID); pErr == nil {
			for _, sp := range svcPerms {
				permissions = appendUniqueStr(permissions, sp.PermissionCode)
			}
		} else {
			h.logger.Debug("auth/me: failed to get local permissions", zap.Error(pErr))
		}
	}

	// A tenant admin/owner must have full access to their tenant's inventory. Surface the
	// inventory_admin role (and superuser flag) explicitly so the UI shows every in-scope
	// page even before the JIT-provisioned local role row has fully propagated — the API
	// middleware already treats inventory_admin as a full bypass.
	isAdmin := claims.IsPlatformOwner || claims.IsSuperuser() ||
		rbac.IsAdminRoles(claims.Roles) || appendUniqueStrContains(roles, rbac.RoleInventoryAdmin)
	if isAdmin {
		roles = appendUniqueStr(roles, rbac.RoleInventoryAdmin)
	}

	respondJSON(w, http.StatusOK, MeResponse{
		ID:              claims.Subject,
		Email:           claims.Email,
		TenantID:        claims.TenantID,
		TenantSlug:      claims.GetTenantSlug(),
		Roles:           roles,
		Permissions:     permissions,
		IsPlatformOwner: claims.IsPlatformOwner,
		IsSuperUser:     claims.IsSuperuser() || isAdmin,
	})
}

// appendUniqueStrContains reports whether slice already contains s.
func appendUniqueStrContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// RegisterAuthRoutes mounts auth routes on the supplied router.
func (h *AuthHandler) RegisterAuthRoutes(r chi.Router) {
	r.Get("/auth/me", h.Me)
}

// appendUniqueStr appends s to slice only if not already present.
func appendUniqueStr(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
