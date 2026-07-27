//go:build ignore

// fix_demo_category_hierarchy.go is a one-off data-fix pass for the codevertex-demo
// tenant's item_categories: bulk-import/seed only ever creates ROOT categories (see
// cmd/seed/seed_categories.go and the doc comment on internal/ent/schema/itemcategory.go),
// so the demo tenant's 29 categories were a flat, unparented list with no hierarchy at
// all — mirroring the same flat structure reported for the boi-enterprises production
// tenant, which was not reachable from this local dev DB.
//
// This groups the ACTUAL existing category rows (queried, never invented) into a
// small set of new parent categories using domain judgment, going through
// items.Service.CreateCategory / UpdateCategory (not raw SQL) so depth/path are
// computed by the same code path production traffic uses.
//
// Idempotent: re-running detects already-created parents by name and already-set
// parent_id on children and skips them.
//
// Run from inventory-api root: go run ./scripts/fix_demo_category_hierarchy.go
// Requires: POSTGRES_URL (default: postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable)
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/tenant"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

// group defines one new parent category and the real, existing child category
// names (as queried from item_categories) that should be re-parented under it.
type group struct {
	parentName string
	parentIcon string
	children   []string
}

var groups = []group{
	{
		parentName: "Beverages",
		parentIcon: "/media/icons/juice-colored.svg",
		children:   []string{"Hot Beverages", "Cold Beverages", "Juice", "Alcohol"},
	},
	{
		parentName: "Food & Dining",
		parentIcon: "/media/icons/burger-colored.svg",
		children: []string{
			"Breakfast", "Main Courses", "Salads", "Sandwiches & Wraps",
			"Pastries & Bakery", "Light Bites", "Accompaniments", "Desserts",
			"Pizza", "Chicken", "Chinese", "Indian", "Sushi",
		},
	},
	{
		parentName: "Retail & Shopping",
		parentIcon: "/media/icons/retail-colored.svg",
		children:   []string{"Retail", "Electronics", "Fashion", "Grocery", "Fresh", "Gifts", "Flowers"},
	},
	{
		parentName: "Health & Beauty",
		parentIcon: "/media/icons/medicine-colored.svg",
		children:   []string{"Pharmacy", "Beauty & Spa"},
	},
	{
		parentName: "Manufacturing Supplies",
		parentIcon: "/media/icons/grocery-colored.svg",
		children:   []string{"Raw Chemicals", "Detergents & Cleaning"},
	},
	// "Events & Experiences" deliberately left as a standalone root: it doesn't
	// fit any of the above groups (per task instructions, genuinely standalone
	// categories stay roots).
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

	tn, err := client.Tenant.Query().Where(tenant.Slug("codevertex-demo")).Only(ctx)
	if err != nil {
		log.Fatalf("find tenant codevertex-demo: %v", err)
	}
	tenantID := tn.ID
	fmt.Printf("tenant codevertex-demo = %s\n", tenantID)

	logger := zap.NewNop()
	svc := items.NewService(client, logger, "")

	// Snapshot existing categories for this tenant, by lowercased name.
	existing, err := client.ItemCategory.Query().
		Where(itemcategory.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		log.Fatalf("query existing categories: %v", err)
	}
	byName := make(map[string]*ent.ItemCategory, len(existing))
	for _, c := range existing {
		byName[strings.ToLower(strings.TrimSpace(c.Name))] = c
	}
	fmt.Printf("found %d existing categories for tenant\n", len(existing))

	for _, g := range groups {
		var parentID *uuid.UUID
		if existingParent, ok := byName[strings.ToLower(strings.TrimSpace(g.parentName))]; ok {
			id := existingParent.ID
			parentID = &id
			fmt.Printf("parent category %q already exists (%s), reusing\n", g.parentName, id)
		} else {
			fmt.Printf("creating parent category %q\n", g.parentName)
			created, err := svc.CreateCategory(ctx, tenantID, items.CategoryDTO{
				Name:     g.parentName,
				Icon:     g.parentIcon,
				IsActive: true,
			})
			if err != nil {
				log.Fatalf("create parent %q: %v", g.parentName, err)
			}
			id := created.ID
			parentID = &id
			// Track it in byName too, in case a later group somehow references it.
			byName[strings.ToLower(strings.TrimSpace(g.parentName))] = &ent.ItemCategory{ID: created.ID, Name: created.Name, ParentID: created.ParentID}
			fmt.Printf("  created %s (depth=%d path=%s)\n", created.ID, created.Depth, created.Path)
		}

		for _, childName := range g.children {
			child, ok := byName[strings.ToLower(strings.TrimSpace(childName))]
			if !ok {
				log.Fatalf("expected existing category %q not found for tenant %s — refusing to invent it", childName, tenantID)
			}
			if child.ParentID != nil && *child.ParentID == *parentID {
				fmt.Printf("  %-24s already parented under %q, skipping\n", childName, g.parentName)
				continue
			}
			updated, err := svc.UpdateCategory(ctx, tenantID, child.ID, items.CategoryDTO{
				Name:     child.Name,
				IsActive: child.IsActive,
				ParentID: parentID,
			})
			if err != nil {
				log.Fatalf("re-parent %q under %q: %v", childName, g.parentName, err)
			}
			fmt.Printf("  %-24s -> parent=%s depth=%d path=%s\n", childName, g.parentName, updated.Depth, updated.Path)
		}
	}

	fmt.Println("done")
}
