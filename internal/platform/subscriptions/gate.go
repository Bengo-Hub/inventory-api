package subscriptions

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

const upgradeURL = "/subscription/upgrade"

// CheckLimit reports whether currentValue is within the plan limit for limitKey.
// Returns true (within limit) when there are no claims, the tenant bypasses
// gating (platform owner / superuser / demo / service-charge), the key is absent,
// or the limit is 0 (treated as unlimited).
func CheckLimit(r *http.Request, limitKey string, currentValue int) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return true
	}
	if claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsDemo || claims.BillingMode == "service_charge" {
		return true
	}
	limit := claims.GetLimit(limitKey)
	if limit == 0 {
		return true
	}
	return currentValue < limit
}

// AssertLimit writes a 402 response when currentValue has reached/exceeded the
// limit for limitKey. Returns true when the limit is exceeded (caller stops).
func AssertLimit(w http.ResponseWriter, r *http.Request, limitKey string, currentValue int) bool {
	if CheckLimit(r, limitKey, currentValue) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       "limit_exceeded",
		"limit_key":   limitKey,
		"upgrade_url": upgradeURL,
	})
	return true
}
