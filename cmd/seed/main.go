package main

import (
	"context"
	"database/sql"
	"log"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
)

func main() {
	_ = godotenv.Load()

	// Prefer the direct PostgreSQL URL (POSTGRES_MIGRATE_URL) to bypass PgBouncer: seed runs
	// schema DDL (client.Schema.Create) which must not go through transaction-pooled PgBouncer.
	dsn := os.Getenv("POSTGRES_MIGRATE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_URL")
	}
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	ctx := context.Background()

	if err := client.Schema.Create(ctx); err != nil {
		log.Fatalf("schema create: %v", err)
	}
	log.Println("schema migrated")

	authURL := os.Getenv("AUTH_API_URL")
	if authURL == "" {
		authURL = "https://sso.codevertexitsolutions.com"
	}
	syncer := tenant.NewSyncer(client, authURL)

	if _, err := syncer.SyncTenant(ctx, "codevertex"); err != nil {
		log.Printf("[SKIP] sync codevertex (platform org): %v", err)
	}

	// Platform-wide globals — must run before any per-tenant role-permission links.
	if err := seedUnits(ctx, client); err != nil {
		log.Fatalf("seed units: %v", err)
	}
	if err := seedGlobalCategories(ctx, client); err != nil {
		log.Fatalf("seed global categories: %v", err)
	}
	if err := seedPermissions(ctx, client); err != nil {
		log.Fatalf("seed permissions: %v", err)
	}
	if err := seedRateLimitConfigs(ctx, client); err != nil {
		log.Fatalf("seed rate limit configs: %v", err)
	}
	if err := seedServiceConfigs(ctx, client); err != nil {
		log.Fatalf("seed service configs: %v", err)
	}

	// Platform-wide RBAC: roles + permissions are SHARED across all tenants (same role => same
	// permissions everywhere). Seeded once, not per tenant. seedRolePermissions reconciles perms onto
	// every role bearing a system code (global or tenant) so none is under-permissioned; seedRoles also
	// consolidates redundant legacy per-tenant duplicates. See feedback_shared_core_reference_data.
	if err := seedRoles(ctx, client); err != nil {
		log.Fatalf("seed roles: %v", err)
	}
	if err := seedRolePermissions(ctx, client); err != nil {
		log.Fatalf("seed role-permissions: %v", err)
	}

	for _, slug := range []string{"codevertex-demo"} {
		tenantID, resolveErr := syncer.SyncTenant(ctx, slug)
		if resolveErr != nil {
			log.Printf("[SKIP] could not resolve %s from auth-api: %v", slug, resolveErr)
			continue
		}
		log.Printf("▶ seeding inventory for tenant: %s (%s)", slug, tenantID)

		catIDs, err := seedItemCategories(ctx, client, tenantID)
		if err != nil {
			log.Fatalf("seed item categories for %s: %v", slug, err)
		}

		unitIDs, err := resolveUnitIDs(ctx, client)
		if err != nil {
			log.Fatalf("resolve unit IDs: %v", err)
		}

		if err := seedItems(ctx, client, tenantID, slug, catIDs, unitIDs); err != nil {
			log.Fatalf("seed items for %s: %v", slug, err)
		}

		if err := seedPricingTiers(ctx, client, tenantID); err != nil {
			log.Fatalf("seed pricing tiers for %s: %v", slug, err)
		}

		if err := seedInventoryConfig(ctx, client, tenantID); err != nil {
			log.Fatalf("seed inventory config for %s: %v", slug, err)
		}

		if err := seedWarehouses(ctx, client, tenantID); err != nil {
			log.Fatalf("seed warehouses for %s: %v", slug, err)
		}

		if err := seedBalances(ctx, client, tenantID, slug); err != nil {
			log.Fatalf("seed balances for %s: %v", slug, err)
		}

		if err := seedEventCapacity(ctx, client, tenantID); err != nil {
			log.Printf("[WARN] seed event capacity for %s: %v", slug, err)
		}

		if err := seedRecipes(ctx, client, tenantID); err != nil {
			log.Fatalf("seed recipes for %s: %v", slug, err)
		}
		recalculateAllRecipeCosts(ctx, client, tenantID)

		// Backfill ItemPricing on the Retail tier from recipe suggested prices so seeded
		// menu items carry a real tier price (parity with the bulk-import behaviour).
		if err := seedItemPricing(ctx, client, tenantID); err != nil {
			log.Printf("[WARN] seed item pricing for %s: %v", slug, err)
		}

		if slug == "codevertex-demo" {
			if err := seedProductionBatches(ctx, client, tenantID); err != nil {
				log.Printf("[WARN] seed production batches for %s: %v", slug, err)
			}
			// Mirror detergent stock into the manufacturing outlet's warehouse (no-op
			// until the demo-manufacturing outlet has synced from auth-api).
			if err := seedManufacturingStock(ctx, client, tenantID); err != nil {
				log.Printf("[WARN] seed manufacturing stock for %s: %v", slug, err)
			}
		}

		if err := seedSubRecipeDemo(ctx, client, tenantID); err != nil {
			log.Printf("[WARN] seed sub-recipe demo for %s: %v", slug, err)
		}

		if slug == "codevertex-demo" {
			if err := seedConferenceBundle(ctx, client, tenantID); err != nil {
				log.Printf("[WARN] seed conference bundle for %s: %v", slug, err)
			}
		}

		if slug == "codevertex-demo" {
			supplierID, err := seedSuppliers(ctx, client, tenantID)
			if err != nil {
				log.Printf("[WARN] seed suppliers for %s: %v", slug, err)
			} else if supplierID != uuid.Nil {
				if err := seedReorderConfig(ctx, client, tenantID, supplierID); err != nil {
					log.Printf("[WARN] seed reorder config for %s: %v", slug, err)
				}
			}
		}

		log.Printf("✅ inventory tenant %s seeded", slug)
	}

	log.Println("seed completed successfully")
}
