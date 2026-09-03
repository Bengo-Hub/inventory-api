// Command seed-demo-equipment idempotently creates a handful of demo biomedical-equipment Asset
// rows for one outlet, so hospital-ui's equipment picker (theatre bookings, ICU episodes, beds)
// has real data to click through on a demo tenant instead of an empty list. Uses the ent client
// directly (mirroring internal/http/handlers/extras_assets.go's CreateAsset shape exactly, minus
// its outbox publish — see below) rather than calling the real HTTP endpoint, since this is a
// standalone one-off DB tool with no JWT to authenticate as.
//
// Deliberately skips the "inventory.asset.created" outbox event CreateAsset normally publishes
// (treasury-api consumes it to auto-register a capital-allowance/depreciation asset) — these are
// lightweight demo rows, not real capital purchases, and registering fake depreciation schedules
// in treasury-api for them would be actively wrong, not just unnecessary.
//
// Usage:
//
//	POSTGRES_URL=... go run ./cmd/seed-demo-equipment -tenant-slug=codevertex-demo -outlet-id=<uuid>
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/asset"
	"github.com/bengobox/inventory-service/internal/ent/tenant"
)

type demoAsset struct {
	name, model, manufacturer, location string
	purchaseCost                        float64
}

var demoAssets = []demoAsset{
	{"Patient Monitor", "IntelliVue MX450", "Philips", "General Ward", 850000},
	{"Infusion Pump", "Infusomat Space", "B. Braun", "General Ward", 180000},
	{"Defibrillator", "R Series", "Zoll", "ICU", 1200000},
	{"Ultrasound Machine", "Voluson E10", "GE Healthcare", "Maternity Ward", 3500000},
	{"Anaesthesia Machine", "Primus", "Dräger", "Theatre", 4200000},
	{"Pulse Oximeter", "Radical-7", "Masimo", "ICU", 120000},
}

func main() {
	slug := flag.String("tenant-slug", "", "tenant slug (required)")
	outletIDStr := flag.String("outlet-id", "", "outlet UUID to assign assets to (required)")
	flag.Parse()
	if *slug == "" || *outletIDStr == "" {
		log.Fatal("seed-demo-equipment: -tenant-slug and -outlet-id are required")
	}
	outletID, err := uuid.Parse(*outletIDStr)
	if err != nil {
		log.Fatalf("seed-demo-equipment: invalid -outlet-id: %v", err)
	}

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		log.Fatal("POSTGRES_URL required")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	defer client.Close()
	ctx := context.Background()

	t, err := client.Tenant.Query().Where(tenant.Slug(*slug)).Only(ctx)
	if err != nil {
		log.Fatalf("seed-demo-equipment: tenant %q not found: %v", *slug, err)
	}
	log.Printf("seeding demo equipment: tenant=%s (id=%s) outlet=%s", t.Slug, t.ID, outletID)

	for _, da := range demoAssets {
		exists, err := client.Asset.Query().
			Where(asset.TenantID(t.ID), asset.OutletID(outletID), asset.Name(da.name), asset.Model(da.model)).
			Exist(ctx)
		if err != nil {
			log.Fatalf("check existing asset %q: %v", da.name, err)
		}
		if exists {
			log.Printf("%q (%s) already exists — skipping", da.name, da.model)
			continue
		}
		tag := "AST-DEMO-" + strings.ToUpper(uuid.New().String()[:8])
		a, err := client.Asset.Create().
			SetTenantID(t.ID).
			SetAssetTag(tag).
			SetName(da.name).
			SetModel(da.model).
			SetManufacturer(da.manufacturer).
			SetLocation(da.location).
			SetCondition("good").
			SetPurchaseCost(da.purchaseCost).
			SetCurrentValue(da.purchaseCost).
			SetBookValue(da.purchaseCost).
			SetOutletID(outletID).
			SetStatus("active").
			SetNotes("Demo equipment — seeded for the Demo Afya Clinic sandbox tenant").
			Save(ctx)
		if err != nil {
			log.Fatalf("create asset %q: %v", da.name, err)
		}
		log.Printf("created %q (%s, tag=%s, id=%s)", a.Name, a.Model, a.AssetTag, a.ID)
	}

	log.Println("seed-demo-equipment: complete")
}
