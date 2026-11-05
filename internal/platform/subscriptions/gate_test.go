package subscriptions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

func limitRequest(limit int) *http.Request {
	claims := &authclient.Claims{TenantID: "t1", SubscriptionLimits: map[string]int{LimitSKU: limit}}
	r := httptest.NewRequest(http.MethodPost, "/items", nil)
	return r.WithContext(authclient.ContextWithClaims(r.Context(), claims))
}

// A token minted before an upgrade must not keep refusing: the refusal asks for the live limit. A
// failed lookup keeps the token's answer, and a request under the cap never asks.
func TestCheckLimitConsultsTheLivePlanOnlyBeforeRefusing(t *testing.T) {
	t.Cleanup(func() { liveLimitLookup.Store(nil) })
	asked := 0
	live, liveOK := 0, false
	SetLiveLimitLookup(func(_ context.Context, tenantID, key string) (int, bool) {
		asked++
		if tenantID != "t1" || key != LimitSKU {
			t.Errorf("lookup for %s %s", tenantID, key)
		}
		return live, liveOK
	})

	if !CheckLimit(limitRequest(2000), LimitSKU, 1999) || asked != 0 {
		t.Fatalf("under the cap: asked %d times, want allowed with no lookup", asked)
	}
	if CheckLimit(limitRequest(2000), LimitSKU, 2000) {
		t.Fatal("a failed lookup must keep the token's refusal")
	}
	live, liveOK = 5000, true
	if !CheckLimit(limitRequest(2000), LimitSKU, 2000) {
		t.Fatal("an upgrade to 5000 must allow item 2001 on a stale token")
	}
	if CheckLimit(limitRequest(2000), LimitSKU, 5000) {
		t.Fatal("the live cap still applies")
	}
	live = -1
	if !CheckLimit(limitRequest(2000), LimitSKU, 9000) {
		t.Fatal("an unlimited plan must allow")
	}

	// The refusal reports the figure it was decided on.
	live = 5000
	w := httptest.NewRecorder()
	if !AssertLimit(w, limitRequest(2000), "products", LimitSKU, 5000) || w.Code != http.StatusPaymentRequired {
		t.Fatalf("assert: code %d", w.Code)
	}
}
