//go:build ignore

// backfill_category_icons.go is a one-off data backfill for categories created before
// InferDefaultCategoryIcon existed (internal/modules/items/category_icon_defaults.go).
// Live CRUD (Service.CreateCategory / UpdateCategory) and bulk-import's ensureCategory now
// auto-assign a default icon on create, so this script only needs to run once per
// environment to fix up pre-existing rows — new rows are already covered going forward.
//
// Idempotent and safe to re-run: it only SELECTs item_categories rows where icon IS NULL
// or icon = ”, and only ever sets the icon column on those exact rows (never touches a
// row that already has a non-empty icon, live or on a second run).
//
// Run from inventory-api root: go run ./scripts/backfill_category_icons.go
// Requires: POSTGRES_URL (default: postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable)
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"database/sql"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/modules/items"
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

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	ctx := context.Background()

	rows, err := client.ItemCategory.Query().
		Where(itemcategory.Or(itemcategory.IconIsNil(), itemcategory.IconEQ(""))).
		All(ctx)
	if err != nil {
		log.Fatalf("query empty-icon categories: %v", err)
	}

	fmt.Printf("found %d categories with an empty icon\n", len(rows))

	updated := 0
	for _, c := range rows {
		useCase := ""
		if len(c.UseCases) > 0 {
			useCase = c.UseCases[0]
		}
		icon := items.InferDefaultCategoryIcon(c.Name, useCase)

		// Re-check-and-set in one UPDATE so a category whose icon was set by some
		// other process between our SELECT and this UPDATE is left untouched.
		n, err := client.ItemCategory.Update().
			Where(
				itemcategory.ID(c.ID),
				itemcategory.Or(itemcategory.IconIsNil(), itemcategory.IconEQ("")),
			).
			SetIcon(icon).
			Save(ctx)
		if err != nil {
			log.Printf("skip category %s (%s): %v", c.ID, c.Name, err)
			continue
		}
		if n > 0 {
			updated++
			fmt.Printf("category %s (%q): icon -> %s\n", c.ID, c.Name, icon)
		}
	}

	fmt.Printf("backfill complete: %d/%d categories updated\n", updated, len(rows))
}
