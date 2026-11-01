package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
)

const (
	demoID   = "982ffb25-0e07-4bbb-8e04-c4c2af2550b6"
	otherID  = "5bce71cd-a29f-484f-adc7-3566aed6d14f"
	demoSlug = "codevertex-demo"
)

func guardStatus(t *testing.T, path string, claims *authclient.Claims, ctxID, ctxSlug string) int {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1/{tenant}", func(tenant chi.Router) {
		tenant.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := req.Context()
				if ctxID != "" {
					ctx = context.WithValue(ctx, httpware.TenantIDKey, ctxID)
				}
				if ctxSlug != "" {
					ctx = context.WithValue(ctx, httpware.TenantSlugKey, ctxSlug)
				}
				if claims != nil {
					ctx = authclient.ContextWithClaims(ctx, claims)
				}
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		tenant.Use(RequireOwnTenant)
		tenant.Get("/inventory/items", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

func TestRequireOwnTenant(t *testing.T) {
	admin := &authclient.Claims{TenantID: demoID, TenantSlug: demoSlug}
	owner := &authclient.Claims{TenantID: demoID, TenantSlug: demoSlug, IsPlatformOwner: true}
	cases := []struct {
		name    string
		path    string
		claims  *authclient.Claims
		ctxID   string
		ctxSlug string
		want    int
	}{
		{"own tenant by slug", "/api/v1/codevertex-demo/inventory/items", admin, demoID, demoSlug, http.StatusOK},
		{"own tenant by id", "/api/v1/" + demoID + "/inventory/items", admin, demoID, "", http.StatusOK},
		{"another tenant's slug in the URL", "/api/v1/urban-loft/inventory/items", admin, "", "urban-loft", http.StatusForbidden},
		{"another tenant's id in the URL", "/api/v1/" + otherID + "/inventory/items", admin, otherID, "", http.StatusForbidden},
		{"another tenant from the X-Tenant-ID header", "/api/v1/codevertex-demo/inventory/items", admin, otherID, demoSlug, http.StatusForbidden},
		{"platform owner may cross tenants", "/api/v1/urban-loft/inventory/items", owner, otherID, "urban-loft", http.StatusOK},
		{"no claims (left to the auth step)", "/api/v1/urban-loft/inventory/items", nil, "", "urban-loft", http.StatusOK},
	}
	for _, c := range cases {
		if got := guardStatus(t, c.path, c.claims, c.ctxID, c.ctxSlug); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
