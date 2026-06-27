package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	entrlc "github.com/bengobox/inventory-service/internal/ent/ratelimitconfig"
	entsvc "github.com/bengobox/inventory-service/internal/ent/serviceconfig"
	"github.com/bengobox/inventory-service/internal/http/handlers"
)

// ---------------------------------------------------------------------------
// Tenant Inventory Config
// ---------------------------------------------------------------------------

func seedInventoryConfig(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	defaults := handlers.DefaultUnitReorderLevels()

	existing, err := client.TenantInventoryConfig.Query().
		Where(entconfig.TenantID(tenantID)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query inventory config: %w", err)
	}

	if ent.IsNotFound(err) {
		_, err = client.TenantInventoryConfig.Create().
			SetTenantID(tenantID).
			SetUnitReorderDefaults(defaults).
			SetNillableDefaultTargetMarginPercent(ptr(30.0)).
			SetRecipesModuleEnabled(true).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create inventory config: %w", err)
		}
		log.Printf("created inventory config for tenant %s", tenantID)
		return nil
	}

	update := existing.Update().SetUnitReorderDefaults(defaults).SetRecipesModuleEnabled(true)
	if existing.DefaultTargetMarginPercent == nil {
		update = update.SetNillableDefaultTargetMarginPercent(ptr(30.0))
	}
	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("update inventory config: %w", err)
	}
	log.Printf("updated inventory config for tenant %s", tenantID)
	return nil
}

// ---------------------------------------------------------------------------
// Rate Limit Configs (platform-level)
// ---------------------------------------------------------------------------

type rateLimitDef struct {
	ServiceName       string
	KeyType           string
	EndpointPattern   string
	RequestsPerWindow int
	WindowSeconds     int
	BurstMultiplier   float64
	Description       string
}

func rateLimitUUID(svc, keyType, pattern string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:ratelimit:%s:%s:%s", svc, keyType, pattern)))
}

var rateLimitDefs = []rateLimitDef{
	{"inventory-api", "global", "*", 1000, 60, 2.0, "Global default: 1000 req/min"},
	{"inventory-api", "tenant", "*", 300, 60, 1.5, "Per-tenant default: 300 req/min"},
	{"inventory-api", "ip", "*", 120, 60, 1.5, "Per-IP default: 120 req/min"},
	{"inventory-api", "user", "*", 60, 60, 1.5, "Per-user default: 60 req/min"},
	{"inventory-api", "endpoint", "/api/v1/*/inventory/items", 200, 60, 2.0, "Items endpoint: 200 req/min"},
	{"inventory-api", "endpoint", "/api/v1/*/inventory/stock/*", 150, 60, 1.5, "Stock endpoints: 150 req/min"},
}

func seedRateLimitConfigs(ctx context.Context, client *ent.Client) error {
	for _, d := range rateLimitDefs {
		id := rateLimitUUID(d.ServiceName, d.KeyType, d.EndpointPattern)
		exists, err := client.RateLimitConfig.Query().
			Where(
				entrlc.ServiceName(d.ServiceName),
				entrlc.KeyType(d.KeyType),
				entrlc.EndpointPattern(d.EndpointPattern),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check rate limit %s/%s/%s: %w", d.ServiceName, d.KeyType, d.EndpointPattern, err)
		}
		if exists {
			continue
		}
		if _, err := client.RateLimitConfig.Create().
			SetID(id).
			SetServiceName(d.ServiceName).
			SetKeyType(d.KeyType).
			SetEndpointPattern(d.EndpointPattern).
			SetRequestsPerWindow(d.RequestsPerWindow).
			SetWindowSeconds(d.WindowSeconds).
			SetBurstMultiplier(d.BurstMultiplier).
			SetIsActive(true).
			SetDescription(d.Description).
			Save(ctx); err != nil {
			return fmt.Errorf("create rate limit %s/%s: %w", d.ServiceName, d.KeyType, err)
		}
		log.Printf("rate limit config created: %s %s %s", d.ServiceName, d.KeyType, d.EndpointPattern)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Service Configs (platform-level defaults)
// ---------------------------------------------------------------------------

type serviceConfigDef struct {
	Key         string
	Value       string
	ConfigType  string
	Description string
	IsSecret    bool
}

func serviceConfigUUID(key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:serviceconfig:"+key))
}

var serviceConfigDefs = []serviceConfigDef{
	{"inventory.max_items_per_category", "500", "int", "Maximum items per category", false},
	{"inventory.max_warehouses_per_tenant", "50", "int", "Maximum warehouses per tenant", false},
	{"inventory.low_stock_threshold_percent", "10", "int", "Low stock alert threshold percentage", false},
	{"inventory.enable_auto_reorder", "false", "bool", "Enable automatic reorder when stock hits reorder level", false},
	{"inventory.reservation_expiry_minutes", "30", "int", "Minutes before unredeemed reservations expire", false},
	{"inventory.enable_batch_tracking", "false", "bool", "Enable batch/lot tracking for items", false},
	{"inventory.default_currency", "KES", "string", "Default currency for cost tracking", false},
	{"inventory.enable_multi_warehouse", "true", "bool", "Enable multi-warehouse support", false},
	{"inventory.screensaver_idle_timeout_seconds", "300", "int", "Idle time (seconds) before the branded screensaver shows; tenant default", false},
}

func seedServiceConfigs(ctx context.Context, client *ent.Client) error {
	for _, d := range serviceConfigDefs {
		id := serviceConfigUUID(d.Key)
		exists, err := client.ServiceConfig.Query().
			Where(
				entsvc.ConfigKey(d.Key),
				entsvc.TenantIDIsNil(),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check service config %s: %w", d.Key, err)
		}
		if exists {
			continue
		}
		if _, err := client.ServiceConfig.Create().
			SetID(id).
			SetConfigKey(d.Key).
			SetConfigValue(d.Value).
			SetConfigType(d.ConfigType).
			SetDescription(d.Description).
			SetIsSecret(d.IsSecret).
			Save(ctx); err != nil {
			return fmt.Errorf("create service config %s: %w", d.Key, err)
		}
		log.Printf("service config created: %s = %s", d.Key, d.Value)
	}
	return nil
}
