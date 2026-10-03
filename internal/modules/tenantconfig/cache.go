// Package tenantconfig serves a tenant's inventory config row from a short-lived per-pod cache.
//
// The stock engine reads the config on every sale line (costing method, non-depleting policy,
// alert opt-in, availability policy). Querying it each time cost several round trips per sale;
// the row changes only when an admin saves Settings, so a 30s per-pod cache is safe: the writing
// pod invalidates immediately and every other pod converges within the TTL.
package tenantconfig

import (
	"context"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenantcfg "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

const ttl = 30 * time.Second

// configs caches the row per tenant. A nil value is cached too, so tenants without a
// config row do not query on every call.
var configs = sharedcache.NewLocal[uuid.UUID, *ent.TenantInventoryConfig](5000, ttl)

// Get returns the tenant's inventory config, or nil when the tenant has none (or the
// lookup failed). Callers must treat the returned row as read-only: it is shared.
func Get(ctx context.Context, client *ent.Client, tenantID uuid.UUID) *ent.TenantInventoryConfig {
	if cfg, ok := configs.Get(tenantID); ok {
		return cfg
	}
	cfg, err := client.TenantInventoryConfig.Query().
		Where(enttenantcfg.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			// Transient failure: do not cache, the next call retries.
			return nil
		}
		cfg = nil
	}
	configs.Set(tenantID, cfg)
	return cfg
}

// Invalidate drops the cached row for a tenant. Call it after any config write.
func Invalidate(tenantID uuid.UUID) {
	configs.Delete(tenantID)
}

// AutoHideOnStockOut reports whether stock-outs may mark items unavailable for the tenant.
// No config row means the platform default: availability is manual-only.
func AutoHideOnStockOut(cfg *ent.TenantInventoryConfig) bool {
	return cfg != nil && cfg.AutoHideOnStockOut
}
