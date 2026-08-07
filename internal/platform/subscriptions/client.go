package subscriptions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	serviceclient "github.com/Bengo-Hub/shared-service-client"
	"go.uber.org/zap"
)

// entitlementCacheTTL bounds how long a tenant's entitlement snapshot is reused by
// event consumers. 60s absorbs event bursts (e.g. POS sale / stock storms) without
// hammering subscriptions-api, while staying fresh enough that a plan change takes
// effect quickly.
const entitlementCacheTTL = 60 * time.Second

// Config holds configuration for the subscriptions S2S client.
type Config struct {
	ServiceURL     string
	APIKey         string
	RequestTimeout time.Duration
}

// Entitlements is the canonical tenant subscription snapshot returned by subscriptions-api's
// GET /api/v1/tenants/{id}/subscription — the same full field set treasury-api's/pos-api's/
// erp-api's platform/subscriptions clients decode, so all fleet subscriptions clients converge
// on one shape instead of each reading a different partial subset (the "drifting shape" bug
// class behind the 2026-08-07 boi-enterprises subscription-gating audit).
type Entitlements struct {
	Features     []string `json:"features"`
	Status       string   `json:"status"`
	BillingMode  string   `json:"billing_mode"`
	IsDemoBypass bool     `json:"is_demo_bypass"`
	// ActiveProducts mirrors treasury-api's Entitlements field of the same name — the
	// per-product self-activation list. Not currently gated on here, decoded for shape parity.
	ActiveProducts []string `json:"active_products"`
	// Limits/PlanCode/TierOrder/AllowOverage/CurrentPeriodEnd/IsPerpetual/Exempt were previously
	// dropped by this DTO even though the S2S response carries them — every inventory PIN-
	// terminal session therefore had claims.SubscriptionLimits permanently nil
	// (CheckLimit/AssertLimit silently fail-open on EVERY structural cap: warehouses, SKUs,
	// suppliers, images-per-item) and no way to tell a real tier/overage/exempt/grace state from
	// a terminal login. Mirrors the fields auth-api's EnrichTokenWithSubscription maps onto an
	// SSO JWT.
	Limits           map[string]int `json:"limits"`
	PlanCode         string         `json:"plan_code"`
	TierOrder        int            `json:"tier_order"`
	AllowOverage     bool           `json:"allow_overage"`
	CurrentPeriodEnd string         `json:"current_period_end"`
	IsPerpetual      bool           `json:"is_perpetual"`
	Exempt           bool           `json:"exempt"`
}

// Client interacts with the subscriptions service over S2S (X-API-Key, no user JWT), built on
// the shared github.com/Bengo-Hub/shared-service-client transport (circuit breaker + bounded
// retry) instead of a bare http.Client.
type Client struct {
	cfg Config
	sc  *serviceclient.Client
}

// NewClient creates a new subscriptions S2S client. May be nil-safe: a nil *Client
// fails open in ConsumerHasFeature so unwired environments never drop data sync. The retry
// budget is kept close to RequestTimeout (not shared-service-client's default 30s budget) since
// this client sits on the cross-service stock-sync gate, which must fail open fast on a
// subscriptions-api outage rather than after a multi-second retry storm.
func NewClient(cfg Config, log *zap.Logger) *Client {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	scCfg := serviceclient.DefaultConfig(strings.TrimRight(cfg.ServiceURL, "/"), "subscriptions-api", log)
	scCfg.Timeout = timeout
	scCfg.InitialInterval = 100 * time.Millisecond
	scCfg.MaxInterval = timeout
	scCfg.MaxElapsedTime = timeout
	return &Client{cfg: cfg, sc: serviceclient.New(scCfg)}
}

// GetEntitlements fetches the tenant's subscription snapshot from the S2S tenant-scoped
// endpoint. subscriptions-api resolves the tenant from the URL param, so API-key auth
// works without a user JWT in context. Returns nil on any error so callers fail open.
func (c *Client) GetEntitlements(ctx context.Context, tenantID string) *Entitlements {
	if c == nil || c.cfg.ServiceURL == "" || tenantID == "" {
		return nil
	}
	headers := map[string]string{"X-Tenant-ID": tenantID}
	if c.cfg.APIKey != "" {
		headers["X-API-Key"] = c.cfg.APIKey
	}
	resp, err := c.sc.Get(ctx, fmt.Sprintf("/api/v1/tenants/%s/subscription", tenantID), headers)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	var e Entitlements
	if err := resp.DecodeJSON(&e); err != nil {
		return nil
	}
	return &e
}

type cachedEntitlements struct {
	ent     *Entitlements
	fetched time.Time
}

var (
	entCacheMu sync.Mutex
	entCache   = map[string]cachedEntitlements{}
)

// ConsumerHasFeature reports whether a tenant is entitled to featureCode, for use by
// NATS event consumers that carry a tenant_id but no user JWT. It mirrors the HTTP-layer
// gating contract:
//
//   - Demo-bypass and service-charge (PAYG) tenants are always allowed.
//   - Otherwise the feature must be present in the tenant's entitlement snapshot.
//
// It FAILS OPEN (returns true) when the client is nil/unwired, subscriptions-api is
// unreachable, or the tenant has no snapshot, so a subscriptions-api outage never
// silently drops legitimate cross-service data sync. Results are cached per tenant for
// entitlementCacheTTL.
func (c *Client) ConsumerHasFeature(ctx context.Context, tenantID, featureCode string) bool {
	if c == nil || tenantID == "" {
		return true // not wired → fail open
	}
	e := c.cachedEntitlements(ctx, tenantID)
	if e == nil {
		return true // lookup failed → fail open
	}
	if e.IsDemoBypass || e.BillingMode == "service_charge" {
		return true // exempt — mirror IsGatingExempt
	}
	for _, f := range e.Features {
		if f == featureCode {
			return true
		}
	}
	return false
}

func (c *Client) cachedEntitlements(ctx context.Context, tenantID string) *Entitlements {
	entCacheMu.Lock()
	if hit, ok := entCache[tenantID]; ok && time.Since(hit.fetched) < entitlementCacheTTL {
		entCacheMu.Unlock()
		return hit.ent
	}
	entCacheMu.Unlock()

	e := c.GetEntitlements(ctx, tenantID)
	if e == nil {
		return nil // do not cache failures — retry next event
	}
	entCacheMu.Lock()
	entCache[tenantID] = cachedEntitlements{ent: e, fetched: time.Now()}
	entCacheMu.Unlock()
	return e
}
