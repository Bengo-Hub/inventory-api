package middleware

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
)

// EnforceOutletAssignment rejects a non-HQ user's request when it targets an
// outlet (via X-Outlet-ID) the user is not assigned to. Mirrors the POS
// outlet_context guard.
//
// Progressive rollout: a user with zero UserOutlet rows is treated as
// unrestricted (pass-through) so existing users aren't locked out before an
// admin assigns their outlets. Once a user has any assignment, the requested
// outlet must be among them.
func EnforceOutletAssignment(client *ent.Client, log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil || claims.IsService || claims.CanAccessAllOutlets() {
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

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{
					"code":    "OUTLET_NOT_ASSIGNED",
					"message": "You are not assigned to this outlet",
				},
			})
		})
	}
}
