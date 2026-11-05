package middleware

import (
	"context"
	"net/http"
	"strings"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

type ctxKey string

const (
	outletIDKey       ctxKey = "outlet_id"
	restrictedUserKey ctxKey = "restricted_user"
)

// OutletContext extracts the outlet context from the request. It must run after authentication
// (it reads the caller's claims) and after EnforceOutletAssignment, which refuses an outlet a
// branch user is not assigned to.
//
// Rules (aligned with pos-api's outlet context):
//   - All-outlet users (CanAccessAllOutletsFor): may pass any X-Outlet-ID or omit it for tenant-wide data
//   - Other users: the X-Outlet-ID they send (already checked against their assignments), else the
//     outlet in their token (a terminal's login outlet, a member's home outlet)
//   - Service accounts (IsService): never restricted, and scoped to the X-Outlet-ID they send.
//     pos-api's catalogue reads per-outlet on_hand this way; dropping the header made every
//     outlet show the tenant-wide quantity (BOI, 2026-11-03). Without a header: tenant-wide.
//   - No claims (an anonymous public catalogue read): the header picks the storefront's outlet
func OutletContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		claims, hasClaims := authclient.ClaimsFromContext(ctx)

		headerOutletID := r.Header.Get("X-Outlet-ID")

		if hasClaims && claims != nil {
			if claims.IsService {
				if headerOutletID != "" {
					ctx = context.WithValue(ctx, outletIDKey, headerOutletID)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if CanAccessAllOutletsFor(claims) {
				if headerOutletID != "" {
					ctx = context.WithValue(ctx, outletIDKey, headerOutletID)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			outlet := headerOutletID
			if outlet == "" {
				outlet = claims.OutletID
			}
			if claims.OutletRestricted() && (outlet == "" || !claims.AllowsOutlet(outlet)) {
				outlet = claims.RestrictedHomeOutlet()
			}
			if outlet != "" {
				ctx = context.WithValue(ctx, outletIDKey, outlet)
			}
			ctx = context.WithValue(ctx, restrictedUserKey, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if headerOutletID != "" {
			ctx = context.WithValue(ctx, outletIDKey, headerOutletID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CanAccessAllOutletsFor reports whether the caller may work in any outlet of the tenant: the
// platform owner, admins and HQ sessions (Claims.CanAccessAllOutlets), and the manager tier, which
// pos-api also treats as tenant-wide (posrole.IsAdminLevel).
func CanAccessAllOutletsFor(claims *authclient.Claims) bool {
	if claims.OutletRestricted() {
		return false // an assigned list outranks the manager tier below
	}
	if claims.CanAccessAllOutlets() {
		return true
	}
	for _, role := range claims.Roles {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "superuser", "owner", "super_admin", "tenant_admin", "administrator",
			"manager", "store_manager", "outlet_manager", "supervisor":
			return true
		}
	}
	return false
}

// GetOutletID retrieves the outlet ID stored in context by OutletContext middleware.
// Returns empty string when no outlet filter is active (all-outlets / HQ mode).
func GetOutletID(ctx context.Context) string {
	v, _ := ctx.Value(outletIDKey).(string)
	return v
}

// IsRestrictedUser returns true when the authenticated user is non-HQ staff
// whose queries must be scoped to their assigned outlet.
func IsRestrictedUser(ctx context.Context) bool {
	v, _ := ctx.Value(restrictedUserKey).(bool)
	return v
}
