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
			if !ok || claims.Subject == "" {
				respondForbidden(w, "unauthorized")
				return
			}

			// Platform owners and superusers bypass all permission checks (JWT source of truth).
			if claims.IsPlatformOwner || claims.IsSuperuser() {
				next.ServeHTTP(w, r)
				return
			}

			// Tenant admins bypass all permission checks (JWT source of truth — no DB round-trip needed).
			if claims.IsAdmin() {
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
			// This runs after RequireAuth so claims are always present here.
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

			if claims.IsPlatformOwner || claims.IsSuperuser() {
				next.ServeHTTP(w, r)
				return
			}

			// Tenant admins bypass all permission checks via JWT (source of truth).
			if claims.IsAdmin() {
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

			// JIT-provision after auth so claims are always present.
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
