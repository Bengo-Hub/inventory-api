// Package middleware provides HTTP middleware for inventory-api.
package middleware

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	httpware "github.com/Bengo-Hub/httpware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RequirePermission returns a middleware that checks whether the authenticated user
// holds the given permission code for the current tenant.
// Requires: authMiddleware.RequireAuth and TenantV2 already applied upstream.
func RequirePermission(svc *rbac.Service, log *zap.Logger, permissionCode string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			claims, ok := authclient.ClaimsFromContext(ctx)
			if !ok {
				respondForbidden(w, "unauthorized")
				return
			}

			// Trusted internal service callers (pos-api, ordering-backend, etc. via
			// requireInternalKeyOrAuth's constant-time X-API-Key compare) carry IsService
			// and deliberately have NO Subject — there is no "user" behind an S2S call, so
			// the claims.Subject=="" check below must never reject them. Before this fix,
			// EVERY S2S caller hitting a perm()-gated /v1/{tenant} route 403'd unconditionally
			// (Subject=="" was rejected before IsPlatformOwner/IsService were even inspected),
			// silently breaking e.g. pos-api's ReverseConsumption call on Delete Sale/reversal
			// — inventory was never actually restored, only the caller never surfaced it loudly
			// since saledelete/reversals treat it as a failed step, not a crash.
			if claims.IsService {
				next.ServeHTTP(w, r)
				return
			}
			if claims.Subject == "" {
				respondForbidden(w, "unauthorized")
				return
			}

			// Platform owners (the platform tenant) bypass all permission checks
			// unconditionally — this is the only blanket bypass kept under strict RBAC.
			if claims.IsPlatformOwner {
				next.ServeHTTP(w, r)
				return
			}

			// Legacy bypass (SEC-1): a tenant superuser/admin used to be auto-permitted just
			// for carrying the global superuser/admin role, with no explicit inventory role.
			// Under strict RBAC (the default) we DROP that bypass: such users must now hold a
			// real inventory role, which JIT provisioning grants them below
			// (superuser/admin -> inventory_admin via assignDefaultRoleFromJWT). The blanket
			// bypass is only restored when INVENTORY_STRICT_RBAC is disabled.
			strict := svc != nil && svc.StrictRBAC()
			if !strict && (claims.IsSuperuser() || claims.IsAdmin()) {
				next.ServeHTTP(w, r)
				return
			}

			tenantIDStr := httpware.GetTenantID(ctx)
			if tenantIDStr == "" {
				tenantIDStr = claims.TenantID
			}
			tenantID, err := uuid.Parse(tenantIDStr)
			if err != nil {
				respondForbidden(w, "invalid tenant")
				return
			}

			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				respondForbidden(w, "invalid user")
				return
			}

			// JIT-provision the user and assign their default inventory role from the JWT.
			// This runs after RequireAuth so claims are always present here. For a tenant
			// superuser/admin this grants the explicit inventory_admin role, so the HasRole
			// check below permits them via REAL RBAC (not a bypass) — no access loss.
			if svc != nil {
				_, _ = svc.EnsureUserFromToken(ctx, tenantID, userID, claims.Email, claims.GetTenantSlug(), claims.Roles...)
			}

			// Check local DB role — inventory_admin bypasses per-route permission checks.
			isAdmin, _ := svc.HasRole(ctx, tenantID, userID, rbac.RoleInventoryAdmin)
			if isAdmin {
				next.ServeHTTP(w, r)
				return
			}

			ok, err = svc.HasPermission(ctx, tenantID, userID, permissionCode)
			if err != nil {
				if log != nil {
					log.Warn("rbac permission check error",
						zap.String("permission", permissionCode),
						zap.Error(err))
				}
				respondForbidden(w, "permission check failed")
				return
			}
			if !ok {
				respondForbidden(w, "insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAnyPermission returns a middleware that passes if the user holds at least
// one of the given permission codes.
func RequireAnyPermission(svc *rbac.Service, log *zap.Logger, permissionCodes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			claims, ok := authclient.ClaimsFromContext(ctx)
			if !ok || claims.Subject == "" {
				respondForbidden(w, "unauthorized")
				return
			}

			// Platform owners bypass unconditionally — the only blanket bypass under strict RBAC.
			if claims.IsPlatformOwner {
				next.ServeHTTP(w, r)
				return
			}

			// Legacy superuser/admin blanket bypass (SEC-1) — only when strict RBAC is disabled.
			// Under strict mode these users go through real RBAC; JIT provisioning below grants
			// them the explicit inventory_admin role so they retain access without a bypass.
			strict := svc != nil && svc.StrictRBAC()
			if !strict && (claims.IsSuperuser() || claims.IsAdmin()) {
				next.ServeHTTP(w, r)
				return
			}

			tenantIDStr := httpware.GetTenantID(ctx)
			if tenantIDStr == "" {
				tenantIDStr = claims.TenantID
			}
			tenantID, err := uuid.Parse(tenantIDStr)
			if err != nil {
				respondForbidden(w, "invalid tenant")
				return
			}

			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				respondForbidden(w, "invalid user")
				return
			}

			// JIT-provision after auth so claims are always present. For superuser/admin this
			// grants the explicit inventory_admin role (real RBAC, not a bypass).
			if svc != nil {
				_, _ = svc.EnsureUserFromToken(ctx, tenantID, userID, claims.Email, claims.GetTenantSlug(), claims.Roles...)
			}

			// inventory_admin bypasses per-route permission checks.
			isAdmin, _ := svc.HasRole(ctx, tenantID, userID, rbac.RoleInventoryAdmin)
			if isAdmin {
				next.ServeHTTP(w, r)
				return
			}

			for _, code := range permissionCodes {
				allowed, _ := svc.HasPermission(ctx, tenantID, userID, code)
				if allowed {
					next.ServeHTTP(w, r)
					return
				}
			}

			respondForbidden(w, "insufficient permissions")
		})
	}
}

func respondForbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
