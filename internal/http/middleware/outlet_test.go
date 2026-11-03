package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/testutil"
)

// outletChain runs EnforceOutletAssignment then OutletContext, as the router's outletScope does,
// and reports the status plus the outlet and restriction the handler saw.
func outletChain(t *testing.T, enforce func(http.Handler) http.Handler, claims *authclient.Claims, header string) (int, string, bool) {
	t.Helper()
	var outlet string
	var restricted bool
	h := enforce(OutletContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outlet, restricted = GetOutletID(r.Context()), IsRestrictedUser(r.Context())
		w.WriteHeader(http.StatusOK)
	})))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shop/inventory/items", nil)
	if header != "" {
		req.Header.Set("X-Outlet-ID", header)
	}
	if claims != nil {
		req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, outlet, restricted
}

func passThrough(next http.Handler) http.Handler { return next }

// Who may work in any outlet, and what everyone else is held to.
func TestOutletContextRules(t *testing.T) {
	home, other := uuid.NewString(), uuid.NewString()
	user := func(roles ...string) *authclient.Claims {
		return &authclient.Claims{TenantID: uuid.NewString(), OutletID: home, Roles: roles,
			RegisteredClaims: jwt.RegisteredClaims{Subject: uuid.NewString()}}
	}
	cases := []struct {
		name           string
		claims         *authclient.Claims
		header         string
		wantOutlet     string
		wantRestricted bool
	}{
		{"admin picks any outlet", user("admin"), other, other, false},
		{"admin without a header sees every outlet", user("admin"), "", "", false},
		{"manager tier picks any outlet, as in POS", user("manager"), other, other, false},
		{"cashier without a header is held to the token's outlet", user("cashier"), "", home, true},
		{"cashier header passes on once assignment is checked", user("cashier"), other, other, true},
		// pos-api reads each outlet's on_hand with its service key and X-Outlet-ID.
		{"service call is scoped to the outlet it names", &authclient.Claims{IsService: true}, other, other, false},
		{"service call without a header is tenant-wide", &authclient.Claims{IsService: true}, "", "", false},
		{"anonymous catalogue read uses the storefront header", nil, other, other, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, outlet, restricted := outletChain(t, passThrough, tc.claims, tc.header)
			if code != http.StatusOK || outlet != tc.wantOutlet || restricted != tc.wantRestricted {
				t.Fatalf("got status %d outlet %q restricted %v, want outlet %q restricted %v",
					code, outlet, restricted, tc.wantOutlet, tc.wantRestricted)
			}
		})
	}
}

// A branch user's outlet header only counts for an outlet they are assigned to; managers are not
// checked, and a user with no assignment yet is not locked out (progressive rollout).
func TestEnforceOutletAssignmentAfterAuth(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_outlet_mw_")
	ctx := context.Background()
	tenantID, cashierID, newcomerID := uuid.New(), uuid.New(), uuid.New()
	assigned, notAssigned := uuid.New(), uuid.New()
	client.UserOutlet.Create().SetTenantID(tenantID).SetUserID(cashierID).SetOutletID(assigned).ExecX(ctx)
	enforce := EnforceOutletAssignment(client, zap.NewNop())
	claimsFor := func(userID uuid.UUID, roles ...string) *authclient.Claims {
		return &authclient.Claims{TenantID: tenantID.String(), Roles: roles,
			RegisteredClaims: jwt.RegisteredClaims{Subject: userID.String()}}
	}

	if code, outlet, _ := outletChain(t, enforce, claimsFor(cashierID, "cashier"), assigned.String()); code != http.StatusOK || outlet != assigned.String() {
		t.Fatalf("assigned outlet: status %d outlet %q", code, outlet)
	}
	// An unassigned outlet is ignored, never refused: the request goes on with the token's outlet.
	if code, outlet, _ := outletChain(t, enforce, claimsFor(cashierID, "cashier"), notAssigned.String()); code != http.StatusOK || outlet != "" {
		t.Fatalf("unassigned outlet: status %d outlet %q, want 200 and no outlet from the header", code, outlet)
	}
	if code, _, _ := outletChain(t, enforce, claimsFor(cashierID, "manager"), notAssigned.String()); code != http.StatusOK {
		t.Fatalf("manager: status %d, want 200", code)
	}
	if code, _, _ := outletChain(t, enforce, claimsFor(newcomerID, "cashier"), notAssigned.String()); code != http.StatusOK {
		t.Fatalf("user with no assignment yet: status %d, want 200", code)
	}
}
