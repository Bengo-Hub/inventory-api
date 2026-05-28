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

	dsn := os.Getenv("POSTGRES_URL")
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
	if err := seedPermissions(ctx, client); err != nil {
		log.Fatalf("seed permissions: %v", err)
	}
	if err := seedRateLimitConfigs(ctx, client); err != nil {
		log.Fatalf("seed rate limit configs: %v", err)
	}
	if err := seedServiceConfigs(ctx, client); err != nil {
		log.Fatalf("seed service configs: %v", err)
	}

	for _, slug := range []string{"urban-loft", "codevertex-demo"} {
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

		if err := seedItems(ctx, client, tenantID, catIDs, unitIDs); err != nil {
			log.Fatalf("seed items for %s: %v", slug, err)
		}

		if err := seedInventoryConfig(ctx, client, tenantID); err != nil {
			log.Fatalf("seed inventory config for %s: %v", slug, err)
		}

		if err := seedWarehouses(ctx, client, tenantID); err != nil {
			log.Fatalf("seed warehouses for %s: %v", slug, err)
		}

		if err := seedBalances(ctx, client, tenantID); err != nil {
			log.Fatalf("seed balances for %s: %v", slug, err)
		}

		if err := seedEventCapacity(ctx, client, tenantID); err != nil {
			log.Printf("[WARN] seed event capacity for %s: %v", slug, err)
		}

		if err := seedRecipes(ctx, client, tenantID); err != nil {
			log.Fatalf("seed recipes for %s: %v", slug, err)
		}
		recalculateAllRecipeCosts(ctx, client, tenantID)

		if err := seedSubRecipeDemo(ctx, client, tenantID); err != nil {
			log.Printf("[WARN] seed sub-recipe demo for %s: %v", slug, err)
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

		if err := seedRoles(ctx, client, tenantID); err != nil {
			log.Fatalf("seed roles for %s: %v", slug, err)
		}
		if err := seedRolePermissions(ctx, client, tenantID); err != nil {
			log.Fatalf("seed role-permissions for %s: %v", slug, err)
		}

		log.Printf("✅ inventory tenant %s seeded", slug)
	}

	log.Println("seed completed successfully")
}
