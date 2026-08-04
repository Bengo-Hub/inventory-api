package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// TestRequirePermission_ServiceCallerBypasses is the regression test for the bug this fix
// closes: an S2S caller (pos-api, ordering-backend — authenticated via requireInternalKeyOrAuth's
// constant-time X-API-Key compare) carries IsService=true and an intentionally EMPTY Subject
// (there is no "user" behind a service call). Before the fix, the Subject=="" check ran BEFORE
// IsService was ever inspected, so every S2S caller hitting a permission-gated /v1/{tenant} route
// 403'd unconditionally — silently breaking pos-api's ReverseConsumption call on Delete Sale.
func TestRequirePermission_ServiceCallerBypasses(t *testing.T) {
	called := false
	handler := RequirePermission(nil, zap.NewNop(), "inventory.consumption.add")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) }),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/inventory/consumption/reverse", nil)
	claims := &authclient.Claims{IsService: true, ServiceName: "platform-internal"}
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatalf("expected the S2S caller to reach the handler, got status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// TestRequirePermission_PlatformOwnerBypasses confirms the pre-existing platform-owner bypass
// still works after the fix (Subject IS set for a real platform-owner user, unlike a service call).
func TestRequirePermission_PlatformOwnerBypasses(t *testing.T) {
	called := false
	handler := RequirePermission(nil, zap.NewNop(), "inventory.items.add")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) }),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/inventory/items", nil)
	claims := &authclient.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "00000000-0000-0000-0000-000000000001"},
		IsPlatformOwner:  true,
		TenantID:         "00000000-0000-0000-0000-000000000002",
	}
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("expected platform owner to bypass and reach the handler, got status %d body %q", rec.Code, rec.Body.String())
	}
}

// TestRequirePermission_NoClaimsRejected confirms an unauthenticated request (no claims at all)
// is still correctly rejected — the fix must not weaken this.
func TestRequirePermission_NoClaimsRejected(t *testing.T) {
	called := false
	handler := RequirePermission(nil, zap.NewNop(), "inventory.items.add")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/inventory/items", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if called {
		t.Fatalf("expected an unauthenticated request to be rejected, but the handler ran")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// TestRequirePermission_EmptySubjectNonServiceRejected confirms a non-service caller with an
// empty Subject (a malformed/incomplete token — never IsService) is still rejected, not
// accidentally swept up by the new IsService bypass.
func TestRequirePermission_EmptySubjectNonServiceRejected(t *testing.T) {
	called := false
	handler := RequirePermission(nil, zap.NewNop(), "inventory.items.add")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/inventory/items", nil)
	claims := &authclient.Claims{} // IsService=false, Subject=""
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if called {
		t.Fatalf("expected an empty-subject non-service caller to be rejected, but the handler ran")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
