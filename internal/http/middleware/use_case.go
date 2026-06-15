package middleware

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// RequireOutletUseCase gates a route to requests whose active outlet's use_case is
// in the allowed set. HQ / platform-owner users bypass the check entirely.
//
// The active outlet's use_case is resolved in priority order:
//  1. the JWT outlet_use_case claim (POS-style enriched tokens), then
//  2. the warehouse mirror of the active outlet — the outlet from the JWT outlet_id
//     claim, the X-Outlet-ID header (validated upstream by EnforceOutletAssignment),
//     or, when neither is present, the user's single UserOutlet assignment.
//
// This makes the gate work for inventory SSO users, whose login token carries no
// outlet claim and who select their outlet via the X-Outlet-ID header. When the
// use_case cannot be determined (no outlet context at all) the request is allowed —
// matching the HQ "all outlets" experience.
//
// Usage in router:
//
//	g.With(middleware.RequireOutletUseCase(orm, log, "hospitality", "quick_service")).Mount("/recipes", ...)
func RequireOutletUseCase(client *ent.Client, log *zap.Logger, allowed ...string) func(http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, uc := range allowed {
		set[uc] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil {
				// Unauthenticated — pass through; auth middleware will reject if needed
				next.ServeHTTP(w, r)
				return
			}

			// HQ and platform owners bypass use-case gating
			if claims.IsPlatformOwner || claims.CanAccessAllOutlets() {
				next.ServeHTTP(w, r)
				return
			}

			useCase := resolveOutletUseCase(r, client, claims)
			if useCase == "" || set[useCase] {
				// No resolvable outlet context, or use_case matches — allow
				next.ServeHTTP(w, r)
				return
			}

			if log != nil {
				log.Debug("outlet use_case gated", zap.String("use_case", useCase))
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

// resolveOutletUseCase determines the active outlet's use_case for a non-HQ request.
// Returns "" when it cannot be determined (treated as permissive by the caller).
func resolveOutletUseCase(r *http.Request, client *ent.Client, claims *authclient.Claims) string {
	// 1) Enriched JWT claim wins (POS PIN tokens carry this).
	if claims.OutletUseCase != "" {
		return claims.OutletUseCase
	}
	if client == nil {
		return ""
	}
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return ""
	}

	// 2) Determine the active outlet id: JWT outlet_id, else the X-Outlet-ID header.
	outletIDStr := claims.OutletID
	if outletIDStr == "" {
		outletIDStr = r.Header.Get("X-Outlet-ID")
	}

	// 3) Fall back to the user's single assignment when no outlet was supplied.
	if outletIDStr == "" {
		userID, uerr := claims.UserID()
		if uerr != nil || userID == uuid.Nil {
			return ""
		}
		rows, qerr := client.UserOutlet.Query().
			Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
			All(r.Context())
		if qerr != nil || len(rows) != 1 {
			return "" // ambiguous (0 or many) — let other guards decide
		}
		outletIDStr = rows[0].OutletID.String()
	}

	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return ""
	}

	// 4) Look up the use_case from the warehouse mirror of that outlet.
	wh, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletID(outletID)).
		First(r.Context())
	if err != nil {
		return ""
	}
	return wh.UseCase
}
