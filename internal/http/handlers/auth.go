package handlers

import (
	"context"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// AuthHandler handles service-level auth sync for inventory-ui.
type AuthHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
	orm         *ent.Client
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(logger *zap.Logger, rbacService *rbac.Service, orm *ent.Client) *AuthHandler {
	return &AuthHandler{
		logger:      logger.Named("auth.Handler"),
		rbacService: rbacService,
		orm:         orm,
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
	// Resolved home outlet for this user in this tenant. Auto-linked to the tenant default
	// when the user had none, so the UI never loads an empty outlet while the tenant has one.
	OutletID   string `json:"outlet_id,omitempty"`
	OutletName string `json:"outlet_name,omitempty"`
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

	// Persist the user's service-level inventory role from their claims on every login, so
	// local RBAC stays in sync for BOTH auth flows: SSO claims carry the global tenant roles,
	// and terminal/PIN claims carry the already-mapped inventory role codes (idempotent). This
	// is the belt-and-braces companion to the auth.user event consumer — a user who reaches the
	// API before their event is processed still gets provisioned. Skipped when claims carry no
	// roles so we never assign a spurious default.
	if h.rbacService != nil && len(claims.Roles) > 0 {
		if _, perr := h.rbacService.EnsureUserFromToken(r.Context(), *tenantID, userID, claims.Email, claims.GetTenantSlug(), claims.Roles...); perr != nil {
			h.logger.Debug("auth/me: service-role sync failed", zap.Error(perr))
		}
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

	// Resolve (and, when missing, auto-link) the user's home outlet so the UI never loads an
	// empty outlet while the tenant has one.
	outletID, outletName := h.resolveHomeOutlet(r.Context(), *tenantID, userID)

	respondJSON(w, http.StatusOK, MeResponse{
		ID:              claims.Subject,
		Email:           claims.Email,
		TenantID:        claims.TenantID,
		TenantSlug:      claims.GetTenantSlug(),
		Roles:           roles,
		Permissions:     permissions,
		IsPlatformOwner: claims.IsPlatformOwner,
		IsSuperUser:     claims.IsSuperuser() || isAdmin,
		OutletID:        outletID,
		OutletName:      outletName,
	})
}

// resolveHomeOutlet returns the user's home outlet (id + name) for the tenant. When the user
// has no outlet assignment yet, it links them to the tenant's default outlet (a warehouse with
// a non-nil outlet_id, preferring is_default) so login is never blocked and the UI always has a
// branch to load. Returns empty strings only when the tenant has no outlet-bearing warehouse.
func (h *AuthHandler) resolveHomeOutlet(ctx context.Context, tenantID, userID uuid.UUID) (string, string) {
	if h.orm == nil {
		return "", ""
	}

	// Already assigned? Prefer the home outlet, else the first assignment.
	rows, err := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(ctx)
	if err == nil && len(rows) > 0 {
		outletID := rows[0].OutletID
		for _, a := range rows {
			if a.IsHomeOutlet {
				outletID = a.OutletID
				break
			}
		}
		return outletID.String(), h.warehouseNameForOutlet(ctx, tenantID, outletID)
	}

	// Unassigned → link to the tenant default outlet.
	wh := h.defaultOutletWarehouse(ctx, tenantID)
	if wh == nil || wh.OutletID == nil {
		return "", ""
	}
	if _, cErr := h.orm.UserOutlet.Create().
		SetTenantID(tenantID).
		SetUserID(userID).
		SetOutletID(*wh.OutletID).
		SetIsHomeOutlet(true).
		Save(ctx); cErr != nil {
		// Non-fatal: still return the default outlet so the UI can load it this session.
		h.logger.Warn("auto-link default outlet failed", zap.Error(cErr),
			zap.String("tenant_id", tenantID.String()), zap.String("user_id", userID.String()))
	} else {
		h.logger.Info("auto-linked user to default outlet",
			zap.String("tenant_id", tenantID.String()), zap.String("user_id", userID.String()),
			zap.String("outlet_id", wh.OutletID.String()))
	}
	return wh.OutletID.String(), wh.Name
}

// defaultOutletWarehouse returns the tenant's default outlet-bearing warehouse: an active
// warehouse with a non-nil outlet_id, preferring is_default. nil when none qualifies.
func (h *AuthHandler) defaultOutletWarehouse(ctx context.Context, tenantID uuid.UUID) *ent.Warehouse {
	base := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.IsActive(true), entwarehouse.OutletIDNotNil())
	if wh, err := base.Clone().Where(entwarehouse.IsDefault(true)).First(ctx); err == nil && wh != nil {
		return wh
	}
	if wh, err := base.First(ctx); err == nil && wh != nil {
		return wh
	}
	return nil
}

// warehouseNameForOutlet resolves a warehouse display name from its outlet id.
func (h *AuthHandler) warehouseNameForOutlet(ctx context.Context, tenantID, outletID uuid.UUID) string {
	if wh, err := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletID(outletID)).
		First(ctx); err == nil && wh != nil {
		return wh.Name
	}
	return ""
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
