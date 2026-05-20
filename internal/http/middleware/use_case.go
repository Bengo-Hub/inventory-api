package middleware

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// RequireOutletUseCase gates a route to requests whose JWT outlet_use_case claim
// matches one of the allowed use-case values. HQ / platform-owner users bypass
// the check so they can access all modules regardless of outlet context.
//
// Usage in router:
//
//	g.With(middleware.RequireOutletUseCase("hospitality", "quick_service")).Mount("/recipes", ...)
//	g.With(middleware.RequireOutletUseCase("retail", "pharmacy")).Mount("/lots", ...)
func RequireOutletUseCase(allowed ...string) func(http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, uc := range allowed {
		set[uc] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok {
				// Unauthenticated — pass through; auth middleware will reject if needed
				next.ServeHTTP(w, r)
				return
			}

			// HQ and platform owners bypass use-case gating
			if claims.IsPlatformOwner || claims.CanAccessAllOutlets() {
				next.ServeHTTP(w, r)
				return
			}

			useCase := claims.OutletUseCase
			if useCase == "" || set[useCase] {
				// No outlet scoping or use_case matches — allow
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":    "feature_not_available",
				"message":  "this feature is not available for your outlet type",
				"use_case": useCase,
			})
		})
	}
}
