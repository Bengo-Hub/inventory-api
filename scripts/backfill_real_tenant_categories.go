//go:build ignore

// backfill_real_tenant_categories.go is a one-off data-fix pass for real (non-demo) tenants
// whose item_categories are a flat, unparented list — the same gap fix_demo_category_hierarchy.go
// closed for codevertex-demo, but for the actual production tenants that script's own comment
// flagged as unreachable from local dev (boi-enterprises, plus alpha-china-market and
// sofain-limited, found via a direct read-only query against the production database).
//
// Groupings below are per-tenant, justified by that tenant's ACTUAL category rows (queried from
// production, never invented) — not a generic template applied uniformly. Categories with no
// natural grouping are deliberately left as standalone roots (see comments per tenant).
//
// Goes through items.Service.CreateCategory / UpdateCategory (not raw SQL) so depth/path are
// computed by the same code path production traffic uses. Idempotent: re-running detects
// already-created parents by name and already-set parent_id on children and skips them.
//
// Run from inventory-api root: go run ./scripts/backfill_real_tenant_categories.go
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
	"github.com/bengobox/inventory-service/internal/ent/tenant"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

// group defines one new parent category and the real, existing child category names (exact,
// as queried from item_categories — case matters, some tenants have case-duplicate rows) that
// should be re-parented under it.
type group struct {
	parentName string
	parentIcon string
	children   []string
}

// tenantPlan is one tenant's full grouping plan.
type tenantPlan struct {
	slug   string
	groups []group
	// ungroupedNote documents categories deliberately left as standalone roots and why —
	// purely informational, not acted on.
	ungroupedNote string
}

var plans = []tenantPlan{
	{
		// General-merchandise/household-goods retailer — matches the real category set behind
		// this session's Alpha China Market / Jumia storefront-layout benchmarking work.
		slug: "alpha-china-market",
		groups: []group{
			{
				parentName: "Kitchen & Dining",
				parentIcon: "/media/icons/kitchenware-colored.svg",
				children:   []string{"Kitchenware", "Utensils", "Serviette Holders", "Bottles & Drinkware"},
			},
			{
				parentName: "Home & Lifestyle",
				parentIcon: "/media/icons/retail-colored.svg",
				children:   []string{"Fragrances & Scents", "Party Supplies"},
			},
		},
	},
	{
		// Phone/electronics/appliance retailer — the flagship "flat categories" example from
		// the ordering-frontend use-case revamp work. Note: this tenant has a genuine
		// case-duplicate pair ("Accessories" / "ACCESSORIES", two distinct rows) — both are
		// parented here as-is; MERGING the duplicate rows is a separate data-quality fix, not
		// attempted by this script.
		slug: "boi-enterprises",
		groups: []group{
			{
				parentName: "Phones & Communication",
				parentIcon: "/media/icons/smartphone-colored.svg",
				children: []string{
					"FEATURE PHONES", "FLIP PHONES", "SMART PHONES", "SMART WATCH",
					"PHONE BATTERIES", "CHARGERS", "EARPHONES", "PODS", "BUDS", "POWERBANKS",
					"Accessories", "ACCESSORIES",
				},
			},
			{
				parentName: "Computing & Storage",
				parentIcon: "/media/icons/laptop-colored.svg",
				children:   []string{"LAPTOPS", "TABLET", "MEMORY", "FLASH", "USB", "HDMI"},
			},
			{
				parentName: "Home Appliances",
				parentIcon: "/media/icons/appliance-colored.svg",
				children: []string{
					"BLENDERS", "FAN", "FREEZER", "FRIDGE", "GAS COOKER", "IRON BOX", "KETTLE",
					"MICROWAVE", "WASHING MACHINE", "DISPENSER", "SHAVERS",
				},
			},
			{
				parentName: "Electronics & Entertainment",
				parentIcon: "/media/icons/television-colored.svg",
				children: []string{
					"CAMERA", "DVR", "RADIOS", "SATELITE", "SPEAKERS", "TELEVISION", "WOOFERS",
					"CONTROLERS", "WALLBRACKET",
				},
			},
			{
				parentName: "Lighting & Power",
				parentIcon: "/media/icons/bulb-colored.svg",
				children: []string{
					"BULBS", "FLOODLIGHT", "RECHARGEABLE LIGHTS", "INVERTORS", "SOLARS",
					"SOLAR BATTERIES", "CAR BATTERIES", "BATTERYS",
				},
			},
			{
				parentName: "Hardware & Security",
				parentIcon: "/media/icons/padlock-colored.svg",
				children:   []string{"PADLOCK", "EXTENSIONS"},
			},
		},
		ungroupedNote: `"COPYS" (photocopier/office-equipment, the only category of its kind) left as a standalone root — a single-item parent isn't a real grouping.`,
	},
	{
		// Butchery/grocery/beverage wholesaler.
		slug: "sofain-limited",
		groups: []group{
			{
				parentName: "Meat & Poultry",
				parentIcon: "/media/icons/meat-colored.svg",
				children:   []string{"Beef", "Fish/Beef", "Kuku"},
			},
			{
				parentName: "Beverages - Alcoholic",
				parentIcon: "/media/icons/wine-colored.svg",
				children:   []string{"Beer", "Cider", "Wine"},
			},
			{
				parentName: "Beverages - Non-Alcoholic",
				parentIcon: "/media/icons/juice-colored.svg",
				children:   []string{"Energy Drinks", "Soft Drinks", "Tea", "Water"},
			},
			{
				parentName: "Groceries & Staples",
				parentIcon: "/media/icons/grocery-colored.svg",
				children:   []string{"Carbohydrates", "Eggs", "Rice"},
			},
		},
		ungroupedNote: `"Extra" left as a standalone root — too vague to justify a grouping.`,
	},
	// Deliberately NOT included (checked, left flat — see the accompanying report):
	// small-steps-cosmetics (4 categories, already distinct/navigable), mss (2 categories),
	// codevertex (3 categories), codevertex-computers (0 categories) — all too few to warrant
	// forcing a hierarchy. codevertex-demo and urban-loft are out of scope entirely: the demo
	// tenant is explicitly excluded by task instructions, and urban-loft already has a real,
	// intentionally-built parent/child tree (14 of 47 categories already parented) from prior
	// menu-hierarchy work — not flat, nothing to fix. __platform_global__ is a separate,
	// higher-blast-radius decision (its rows are is_global=true, shared across every tenant) not
	// bundled into this per-tenant pass.
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

	for _, plan := range plans {
		fmt.Printf("\n=== tenant %s ===\n", plan.slug)
		if err := applyPlan(ctx, client, svc, plan); err != nil {
			log.Fatalf("tenant %s: %v", plan.slug, err)
		}
	}

	fmt.Println("\ndone")
}

func applyPlan(ctx context.Context, client *ent.Client, svc *items.Service, plan tenantPlan) error {
	tn, err := client.Tenant.Query().Where(tenant.Slug(plan.slug)).Only(ctx)
	if err != nil {
		return fmt.Errorf("find tenant: %w", err)
	}
	tenantID := tn.ID
	fmt.Printf("tenant %s = %s\n", plan.slug, tenantID)

	// Snapshot existing categories by EXACT name (not lowercased) — some tenants (boi-enterprises)
	// have genuine case-duplicate rows ("Accessories" vs "ACCESSORIES") that a lowercased map
	// would silently collapse into one, leaving the other untouched.
	existing, err := client.ItemCategory.Query().
		Where(itemcategory.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query existing categories: %w", err)
	}
	byExactName := make(map[string]*ent.ItemCategory, len(existing))
	for _, c := range existing {
		byExactName[c.Name] = c
	}
	fmt.Printf("found %d existing categories for tenant\n", len(existing))

	for _, g := range plan.groups {
		var parentID *uuid.UUID
		if existingParent, ok := byExactName[g.parentName]; ok {
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
				return fmt.Errorf("create parent %q: %w", g.parentName, err)
			}
			id := created.ID
			parentID = &id
			byExactName[created.Name] = &ent.ItemCategory{ID: created.ID, Name: created.Name, ParentID: created.ParentID}
			fmt.Printf("  created %s (depth=%d path=%s)\n", created.ID, created.Depth, created.Path)
		}

		for _, childName := range g.children {
			child, ok := byExactName[childName]
			if !ok {
				return fmt.Errorf("expected existing category %q not found for tenant %s — refusing to invent it", childName, tenantID)
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
				return fmt.Errorf("re-parent %q under %q: %w", childName, g.parentName, err)
			}
			fmt.Printf("  %-24s -> parent=%s depth=%d path=%s\n", childName, g.parentName, updated.Depth, updated.Path)
		}
	}
	if plan.ungroupedNote != "" {
		fmt.Printf("note: %s\n", plan.ungroupedNote)
	}
	return nil
}
