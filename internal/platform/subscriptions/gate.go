package subscriptions

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

const upgradeURL = "/settings?tab=subscription"

// Inventory plan-limit keys (must match subscription-service seeds — these are inventory_*
// prefixed, NOT bare max_*). All are structural caps: hard-block, no overage.
const (
	LimitWarehouses = "inventory_max_warehouses"
	LimitSKU        = "inventory_max_sku"
	LimitSuppliers  = "max_suppliers"
)

// CheckLimit reports whether currentValue is within the plan limit for limitKey.
// Returns true (within limit) when there are no claims, the tenant bypasses
// gating (platform owner / superuser / demo / service-charge), the key is absent,
// or the limit is <= 0 (treated as unlimited).
func CheckLimit(r *http.Request, limitKey string, currentValue int) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return true
	}
	if claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsDemo || claims.BillingMode == "service_charge" {
		return true
	}
	limit := claims.GetLimit(limitKey)
	if limit <= 0 {
		return true
	}
	return currentValue < limit
}

// AssertLimit writes a structured 402 response when currentValue has reached/exceeded the
// plan limit for limitKey. Returns true when the limit is exceeded (caller stops). The
// `metric` is the UI-facing name (e.g. "warehouses", "products"); the body matches the
// shared limit-reached modal contract. Inventory caps are structural → overage_eligible=false.
func AssertLimit(w http.ResponseWriter, r *http.Request, metric, limitKey string, currentValue int) bool {
	if CheckLimit(r, limitKey, currentValue) {
		return false
	}
	limit := 0
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
		limit = claims.GetLimit(limitKey)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":             "usage_limit_exceeded",
		"error":            "usage_limit_exceeded",
		"message":          "You've reached your plan's " + metric + " limit.",
		"metric":           metric,
		"limit":            limit,
		"used":             currentValue,
		"overage_eligible": false,
		"limit_key":        limitKey,
		"upgrade_url":      upgradeURL,
	})
	return true
}
