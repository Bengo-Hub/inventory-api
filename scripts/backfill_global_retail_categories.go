//go:build ignore

// backfill_global_retail_categories.go groups the platform's shared GLOBAL "retail" categories
// (tenant_id=nil, is_global=true) into a real parent/child hierarchy. These are the categories
// most retail tenants' actual catalogs are built from (confirmed by querying production: e.g.
// alpha-china-market's entire 1,673-item catalog lives under these 23 global rows, not under any
// tenant-owned category) — a prior backfill pass (backfill_real_tenant_categories.go) fixed
// hierarchy for TENANT-OWNED categories, which turned out to be dead/unused data for some tenants
// (alpha-china-market: 0 items; sofain-limited: partially used). This pass fixes the categories
// that are actually driving storefront catalogs for retail tenants on the shared template.
//
// Grouping mirrors the real top-level departments already used on the reference storefront
// (Home & Living, Kitchen & Dining, Fashion & Accessories, Baby & Kid Products, Electronics,
// Toys & Games, Food & Beverage) — queried categories only, never invented. Categories that
// already match a reference department 1:1 with no further sub-items to group (Baby & Kids,
// Electronics, Toys & Games, Stationery & Office) are left as standalone roots rather than
// wrapped in a redundant single-child parent.
//
// Blast radius: this changes a SHARED resource visible to every tenant using the global retail
// category template, not one tenant's own data — reviewed and approved before running.
//
// Goes through items.Service.CreateCategory / UpdateCategory (not raw SQL), same as prior
// backfills, so depth/path are computed by the same code path production traffic uses.
// Idempotent: re-running detects already-created parents by name and already-set parent_id on
// children and skips them.
//
// Run from inventory-api root: go run ./scripts/backfill_global_retail_categories.go
// Requires: POSTGRES_URL (picked up from the process environment — never pass it on the command
// line or log it).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

type group struct {
	parentName string
	parentIcon string
	children   []string
}

var groups = []group{
	{
		parentName: "Home & Living",
		parentIcon: "/media/icons/home-colored.svg",
		children: []string{
			"Cleaning & Household", "Fragrances & Air Care", "Furniture & Fittings",
			"General Goods", "General Merchandise", "Holders & Organizers", "Home Decor",
			"Packaging", "Party & Gifts",
		},
	},
	{
		parentName: "Kitchen & Dining",
		parentIcon: "/media/icons/kitchenware-colored.svg",
		children: []string{
			"Bottles, Flasks & Drinkware", "Kitchenware & Cookware",
			"Small Appliances & Electronics", "Tableware & Cutlery",
		},
	},
	{
		parentName: "Fashion & Accessories",
		parentIcon: "/media/icons/apparel-colored.svg",
		children: []string{
			"Apparel & Accessories", "Bags & Luggage", "Footwear", "Watches & Fashion Accessories",
		},
	},
	{
		parentName: "Food & Beverage",
		parentIcon: "/media/icons/grocery-colored.svg",
		children:   []string{"Confectionery & Snacks"},
	},
	{
		parentName: "Health & Beauty",
		parentIcon: "/media/icons/medicine-colored.svg",
		children:   []string{"Personal Care & Cosmetics"},
	},
	// Deliberately left as standalone roots (already match a real top-level department 1:1,
	// with no further sibling categories to group under them): Baby & Kids, Electronics,
	// Toys & Games, Stationery & Office.
}

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
	logger := zap.NewNop()
	svc := items.NewService(client, logger, "")

	tenantID := uuid.Nil // global categories: tenant_id = nil, is_global = true

	existing, err := client.ItemCategory.Query().
		Where(itemcategory.TenantID(tenantID), itemcategory.IsGlobal(true)).
		All(ctx)
	if err != nil {
		log.Fatalf("query existing global categories: %v", err)
	}
	byExactName := make(map[string]*ent.ItemCategory, len(existing))
	for _, c := range existing {
		byExactName[c.Name] = c
	}
	fmt.Printf("found %d existing global categories\n", len(existing))

	for _, g := range groups {
		var parentID *uuid.UUID
		if existingParent, ok := byExactName[g.parentName]; ok {
			id := existingParent.ID
			parentID = &id
			fmt.Printf("parent category %q already exists (%s), reusing\n", g.parentName, id)
		} else {
			fmt.Printf("creating global parent category %q\n", g.parentName)
			created, err := svc.CreateCategory(ctx, tenantID, items.CategoryDTO{
				Name:     g.parentName,
				Icon:     g.parentIcon,
				IsActive: true,
				IsGlobal: true,
				UseCases: []string{"retail"},
			})
			if err != nil {
				log.Fatalf("create parent %q: %v", g.parentName, err)
			}
			id := created.ID
			parentID = &id
			byExactName[created.Name] = &ent.ItemCategory{ID: created.ID, Name: created.Name, ParentID: created.ParentID}
			fmt.Printf("  created %s (depth=%d path=%s)\n", created.ID, created.Depth, created.Path)
		}

		for _, childName := range g.children {
			child, ok := byExactName[childName]
			if !ok {
				log.Fatalf("expected existing global category %q not found — refusing to invent it", childName)
			}
			if child.ParentID != nil && *child.ParentID == *parentID {
				fmt.Printf("  %-32s already parented under %q, skipping\n", childName, g.parentName)
				continue
			}
			updated, err := svc.UpdateCategory(ctx, tenantID, child.ID, items.CategoryDTO{
				Name:     child.Name,
				IsActive: child.IsActive,
				IsGlobal: true,
				ParentID: parentID,
			})
			if err != nil {
				log.Fatalf("re-parent %q under %q: %v", childName, g.parentName, err)
			}
			fmt.Printf("  %-32s -> parent=%s depth=%d path=%s\n", childName, g.parentName, updated.Depth, updated.Path)
		}
	}

	fmt.Println("done")
}
