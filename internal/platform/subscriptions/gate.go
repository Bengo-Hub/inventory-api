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
	// LimitImagesPerItem caps how many IMAGE assets a single catalog item may carry.
	// Structural cap (hard-block, no overage). Defaults to 1 when the plan is silent —
	// see DefaultMaxImagesPerItem — so a tenant on a plan without the multi-image feature
	// can still keep exactly one (primary) image, preserving the legacy single-image flow.
	LimitImagesPerItem = "inventory_max_images_per_item"
)

// FeatureMultipleImages is the entitlement that unlocks attaching MORE than the single
// primary image to a catalog item. The first image is always allowed (legacy parity);
// only the 2nd+ image is gated behind this feature.
const FeatureMultipleImages = "inventory_multiple_images"

// DefaultMaxImagesPerItem is the per-item IMAGE cap applied when the plan does not set
// inventory_max_images_per_item. One image keeps the legacy single-image behaviour intact
// for tenants whose plan predates / omits the multi-image limit.
const DefaultMaxImagesPerItem = 1

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

// HasFeature reports whether the tenant is entitled to featureCode. Returns true (allowed)
// when there are no claims or the tenant is gating-exempt (platform owner / demo / service-charge
// / explicitly exempt), mirroring CheckLimit's fail-open posture for un-authed / exempt callers.
func HasFeature(r *http.Request, featureCode string) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return true
	}
	if claims.IsGatingExempt() {
		return true
	}
	return claims.HasFeature(featureCode)
}

// EffectiveImageLimit resolves the per-item IMAGE cap for the requesting tenant. Gating-exempt
// tenants (and un-authed/S2S callers) get effectively unlimited; otherwise the plan's
// inventory_max_images_per_item is used, falling back to DefaultMaxImagesPerItem (1) when the
// plan is silent, so the legacy single-image behaviour is always available.
func EffectiveImageLimit(r *http.Request) int {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return 0 // unlimited (treated as no cap by AssertImageLimit)
	}
	if claims.IsGatingExempt() {
		return 0
	}
	limit := claims.GetLimit(LimitImagesPerItem)
	if limit <= 0 {
		return DefaultMaxImagesPerItem
	}
	return limit
}

// AssertImageLimit writes a structured 402 when the tenant's current IMAGE count for an item has
// reached the effective per-item image cap. Unlike AssertLimit it applies the DefaultMaxImagesPerItem
// floor (so a missing plan limit means 1, not unlimited). Returns true when the cap is hit (caller stops).
func AssertImageLimit(w http.ResponseWriter, r *http.Request, currentValue int) bool {
	limit := EffectiveImageLimit(r)
	if limit <= 0 || currentValue < limit {
		return false // unlimited, or still within cap
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":             "usage_limit_exceeded",
		"error":            "usage_limit_exceeded",
		"message":          "You've reached your plan's images-per-item limit.",
		"metric":           "images_per_item",
		"limit":            limit,
		"used":             currentValue,
		"overage_eligible": false,
		"limit_key":        LimitImagesPerItem,
		"upgrade_url":      upgradeURL,
	})
	return true
}

// AssertFeature writes a structured 403 feature-lock response when the tenant lacks featureCode.
// Returns true when the feature is locked (caller stops). Mirrors the shared feature-gate contract.
func AssertFeature(w http.ResponseWriter, r *http.Request, featureCode, metric string) bool {
	if HasFeature(r, featureCode) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":        "feature_not_available",
		"error":       "feature_not_available",
		"message":     "Your plan does not include " + metric + ".",
		"feature":     featureCode,
		"upgrade_url": upgradeURL,
	})
	return true
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
