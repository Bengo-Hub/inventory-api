package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// RequireOwnTenant refuses a signed-in caller whose token belongs to another tenant than the one
// the request is for. TenantV2 runs before authentication on the /{tenant} routes (the PIN login
// there must stay public), so it cannot see the token and would accept an X-Tenant-ID header or a
// URL naming any tenant. This check runs after RequireAnyAuth: the URL tenant and any tenant
// resolved into the context must be the token's own. Platform owners may work across tenants.
func RequireOwnTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := authclient.ClaimsFromContext(r.Context())
		if ok && !claims.IsPlatformOwner && !tenantMatchesClaims(r, claims.TenantID, claims.GetTenantSlug()) {
			// Which value disagreed: a refused request otherwise leaves no trace (2026-11-03,
			// demo sign-in stuck on 403 with no way to tell the cause from the logs).
			log.Printf("tenant_mismatch path=%s url_tenant=%q ctx_tenant_id=%q ctx_tenant_slug=%q token_tenant_id=%q token_tenant_slug=%q",
				r.URL.Path, chi.URLParam(r, "tenant"), httpware.GetTenantID(r.Context()), httpware.GetTenantSlug(r.Context()),
				claims.TenantID, claims.GetTenantSlug())
			http.Error(w, `{"error":"tenant_mismatch","message":"this session belongs to a different organisation"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tenantMatchesClaims reports whether every tenant the request names (URL segment, resolved
// context id and slug) is the token's tenant. A value the token cannot be compared with is
// skipped, never assumed to match a different tenant.
func tenantMatchesClaims(r *http.Request, claimID, claimSlug string) bool {
	same := func(supplied, claimed string) bool {
		return supplied == "" || claimed == "" || strings.EqualFold(supplied, claimed)
	}
	if param := chi.URLParam(r, "tenant"); param != "" {
		if _, err := uuid.Parse(param); err == nil {
			if !same(param, claimID) {
				return false
			}
		} else if !same(param, claimSlug) {
			return false
		}
	}
	return same(httpware.GetTenantID(r.Context()), claimID) && same(httpware.GetTenantSlug(r.Context()), claimSlug)
}
