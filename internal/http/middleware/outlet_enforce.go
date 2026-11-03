package middleware

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
)

// EnforceOutletAssignment drops a branch user's X-Outlet-ID when it names an
// outlet the user is not assigned to, so OutletContext falls back to the outlet
// in their token. It must run after authentication: it reads the claims.
// (Refusing with 403 locked branch users out of the whole app on 2026-11-03:
// inventory-ui sends the top-nav outlet on every request, /auth/me included.)
//
// Progressive rollout: a user with zero UserOutlet rows is treated as
// unrestricted (pass-through) so existing users aren't locked out before an
// admin assigns their outlets. Once a user has any assignment, only an outlet
// among them is honoured.
func EnforceOutletAssignment(client *ent.Client, log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil || claims.IsService || CanAccessAllOutletsFor(claims) {
				next.ServeHTTP(w, r)
				return
			}
			headerOutletID := r.Header.Get("X-Outlet-ID")
			if headerOutletID == "" {
				next.ServeHTTP(w, r)
				return
			}
			outletID, err := uuid.Parse(headerOutletID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			tenantID, err := uuid.Parse(claims.TenantID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			userID, err := claims.UserID()
			if err != nil || userID == uuid.Nil {
				next.ServeHTTP(w, r)
				return
			}

			rows, err := client.UserOutlet.Query().
				Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
				All(r.Context())
			if err != nil {
				log.Warn("outlet assignment check failed; allowing request", zap.Error(err))
				next.ServeHTTP(w, r)
				return
			}
			if len(rows) == 0 {
				// Not yet restricted — no assignments configured for this user.
				next.ServeHTTP(w, r)
				return
			}
			for _, a := range rows {
				if a.OutletID == outletID {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Not assigned: ignore the requested outlet rather than refuse the request, so the
			// app keeps working (sign-in, /auth/me) while OutletContext falls back to the
			// outlet in the token. The user still never sees another outlet's data.
			log.Debug("ignoring X-Outlet-ID the user is not assigned to",
				zap.String("user_id", userID.String()), zap.String("outlet_id", outletID.String()))
			r.Header.Del("X-Outlet-ID")
			next.ServeHTTP(w, r)
		})
	}
}
