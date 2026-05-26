package middleware

import (
	"context"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

type ctxKey string

const (
	outletIDKey        ctxKey = "outlet_id"
	restrictedUserKey  ctxKey = "restricted_user"
)

// OutletContext extracts the outlet context from the request.
//
// Rules:
//   - HQ users (CanAccessAllOutlets): may pass any X-Outlet-ID or omit for tenant-wide data
//   - Non-HQ users: cannot override the outlet via header; outlet from their JWT claim is used
//   - Service accounts (IsService): pass through without outlet restriction
func OutletContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		claims, hasClaims := authclient.ClaimsFromContext(ctx)

		headerOutletID := r.Header.Get("X-Outlet-ID")

		if hasClaims && claims != nil {
			if claims.IsService {
				// Service accounts bypass outlet filtering
				next.ServeHTTP(w, r)
				return
			}

			if claims.CanAccessAllOutlets() {
				// HQ/admin: honour X-Outlet-ID header for drill-down; empty = all outlets
				if headerOutletID != "" {
					ctx = context.WithValue(ctx, outletIDKey, headerOutletID)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Non-HQ user: use their outlet from JWT claims; ignore any override header
			if claims.OutletID != "" {
				ctx = context.WithValue(ctx, outletIDKey, claims.OutletID)
			}
			ctx = context.WithValue(ctx, restrictedUserKey, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// No claims (shouldn't reach here in authenticated routes, but be safe)
		if headerOutletID != "" {
			ctx = context.WithValue(ctx, outletIDKey, headerOutletID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
