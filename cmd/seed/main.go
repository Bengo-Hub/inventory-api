package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
)


func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("INVENTORY_POSTGRES_URL")
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

	// Resolve tenant UUID and upsert tenant row.
	syncer := tenant.NewSyncer(client)
	tenantID, resolveErr := syncer.SyncTenant(ctx, "urban-loft")
	if resolveErr != nil {
		log.Fatalf("[FATAL] Could not resolve urban-loft UUID from auth-api: %v\nRun auth-api seed before inventory-api seed.", resolveErr)
	}

	log.Printf("seeding with tenant_id = %s (urban-loft)", tenantID)

	if err := seedWarehouse(ctx, client, tenantID); err != nil {
		log.Fatalf("seed warehouse: %v", err)
	}

	if err := seedItems(ctx, client, tenantID); err != nil {
		log.Fatalf("seed items: %v", err)
	}

	if err := seedBalances(ctx, client, tenantID); err != nil {
		log.Fatalf("seed balances: %v", err)
	}

	log.Println("seed completed successfully")
}

func seedWarehouse(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	exists, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.Code("MAIN")).
		Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		log.Println("warehouse MAIN already exists, skipping")
		return nil
	}

	_, err = client.Warehouse.Create().
		SetTenantID(tenantID).
		SetName("Urban Loft Busia Kitchen").
		SetCode("MAIN").
		SetAddress("Busia, Kenya").
		SetIsDefault(true).
		SetIsActive(true).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create warehouse: %w", err)
	}
	log.Println("warehouse MAIN created")
	return nil
}

type itemDef struct {
	SKU           string
	Name          string
	Description   string
	Category      string
	Price         float64
	UnitOfMeasure string
	OnHand        int
}

var catalogItems = []itemDef{
	// Hot Beverages
	{SKU: "BEV-ESP-001", Name: "Espresso", Description: "Single shot of rich espresso", Category: "hot-beverages", Price: 250, UnitOfMeasure: "cup", OnHand: 500},
	{SKU: "BEV-ESP-002", Name: "Double Espresso", Description: "Double shot espresso", Category: "hot-beverages", Price: 350, UnitOfMeasure: "cup", OnHand: 500},
	{SKU: "BEV-LAT-001", Name: "Caffe Latte", Description: "Espresso with steamed milk", Category: "hot-beverages", Price: 400, UnitOfMeasure: "cup", OnHand: 400},
	{SKU: "BEV-CAP-001", Name: "Cappuccino", Description: "Espresso with frothed milk and cocoa", Category: "hot-beverages", Price: 400, UnitOfMeasure: "cup", OnHand: 400},
	{SKU: "BEV-AME-001", Name: "Americano", Description: "Espresso with hot water", Category: "hot-beverages", Price: 300, UnitOfMeasure: "cup", OnHand: 500},
	{SKU: "BEV-MOC-001", Name: "Mocha", Description: "Espresso, chocolate, steamed milk, whipped cream", Category: "hot-beverages", Price: 450, UnitOfMeasure: "cup", OnHand: 300},
	{SKU: "BEV-MAC-001", Name: "Macchiato", Description: "Espresso with a dash of milk foam", Category: "hot-beverages", Price: 350, UnitOfMeasure: "cup", OnHand: 400},
	{SKU: "BEV-TEA-001", Name: "Kenya AA Black Tea", Description: "Premium Kenyan black tea", Category: "hot-beverages", Price: 200, UnitOfMeasure: "cup", OnHand: 600},
	{SKU: "BEV-TEA-002", Name: "Masala Chai", Description: "Spiced tea latte with cardamom and ginger", Category: "hot-beverages", Price: 300, UnitOfMeasure: "cup", OnHand: 400},
	{SKU: "BEV-HOT-001", Name: "Hot Chocolate", Description: "Rich hot chocolate with whipped cream", Category: "hot-beverages", Price: 400, UnitOfMeasure: "cup", OnHand: 300},

	// Cold Beverages
	{SKU: "BEV-ICE-001", Name: "Iced Latte", Description: "Chilled espresso with cold milk over ice", Category: "cold-beverages", Price: 450, UnitOfMeasure: "cup", OnHand: 300},
	{SKU: "BEV-ICE-002", Name: "Iced Americano", Description: "Espresso over ice with cold water", Category: "cold-beverages", Price: 350, UnitOfMeasure: "cup", OnHand: 300},
	{SKU: "BEV-FRP-001", Name: "Caramel Frappe", Description: "Blended iced coffee with caramel drizzle", Category: "cold-beverages", Price: 500, UnitOfMeasure: "cup", OnHand: 200},
	{SKU: "BEV-FRP-002", Name: "Vanilla Frappe", Description: "Blended iced coffee with vanilla", Category: "cold-beverages", Price: 500, UnitOfMeasure: "cup", OnHand: 200},
	{SKU: "BEV-SMO-001", Name: "Mango Smoothie", Description: "Fresh mango blended with yoghurt", Category: "cold-beverages", Price: 450, UnitOfMeasure: "cup", OnHand: 150},
	{SKU: "BEV-SMO-002", Name: "Mixed Berry Smoothie", Description: "Strawberry, blueberry, and banana blend", Category: "cold-beverages", Price: 500, UnitOfMeasure: "cup", OnHand: 150},
	{SKU: "BEV-JCE-001", Name: "Fresh Orange Juice", Description: "Freshly squeezed orange juice", Category: "cold-beverages", Price: 350, UnitOfMeasure: "cup", OnHand: 200},

	// Pastries & Bakery
	{SKU: "PST-CRO-001", Name: "Butter Croissant", Description: "Flaky French butter croissant", Category: "pastries", Price: 250, UnitOfMeasure: "piece", OnHand: 100},
	{SKU: "PST-CRO-002", Name: "Chocolate Croissant", Description: "Croissant filled with dark chocolate", Category: "pastries", Price: 300, UnitOfMeasure: "piece", OnHand: 80},
	{SKU: "PST-MUF-001", Name: "Blueberry Muffin", Description: "Moist muffin loaded with blueberries", Category: "pastries", Price: 280, UnitOfMeasure: "piece", OnHand: 80},
	{SKU: "PST-MUF-002", Name: "Banana Walnut Muffin", Description: "Banana muffin with crunchy walnuts", Category: "pastries", Price: 280, UnitOfMeasure: "piece", OnHand: 80},
	{SKU: "PST-CKE-001", Name: "Carrot Cake Slice", Description: "Spiced carrot cake with cream cheese frosting", Category: "pastries", Price: 350, UnitOfMeasure: "slice", OnHand: 50},
	{SKU: "PST-CKE-002", Name: "Red Velvet Cake Slice", Description: "Classic red velvet with vanilla cream cheese", Category: "pastries", Price: 400, UnitOfMeasure: "slice", OnHand: 40},
	{SKU: "PST-CKE-003", Name: "Chocolate Fudge Cake Slice", Description: "Rich chocolate fudge layer cake", Category: "pastries", Price: 400, UnitOfMeasure: "slice", OnHand: 40},
	{SKU: "PST-DAN-001", Name: "Danish Pastry", Description: "Flaky pastry with custard and fruit", Category: "pastries", Price: 300, UnitOfMeasure: "piece", OnHand: 60},
	{SKU: "PST-SCO-001", Name: "Classic Scone", Description: "Buttermilk scone with clotted cream and jam", Category: "pastries", Price: 250, UnitOfMeasure: "piece", OnHand: 70},

	// Sandwiches & Wraps
	{SKU: "SND-CLB-001", Name: "Club Sandwich", Description: "Triple-decker with chicken, bacon, lettuce, tomato", Category: "sandwiches", Price: 650, UnitOfMeasure: "piece", OnHand: 60},
	{SKU: "SND-GRL-001", Name: "Grilled Chicken Panini", Description: "Grilled chicken, pesto, mozzarella on ciabatta", Category: "sandwiches", Price: 600, UnitOfMeasure: "piece", OnHand: 50},
	{SKU: "SND-VEG-001", Name: "Veggie Wrap", Description: "Hummus, avocado, roasted vegetables in tortilla", Category: "sandwiches", Price: 500, UnitOfMeasure: "piece", OnHand: 50},
	{SKU: "SND-BLT-001", Name: "BLT Sandwich", Description: "Bacon, lettuce, tomato on toasted sourdough", Category: "sandwiches", Price: 550, UnitOfMeasure: "piece", OnHand: 50},
	{SKU: "SND-TUN-001", Name: "Tuna Melt", Description: "Tuna salad with melted cheddar on rye bread", Category: "sandwiches", Price: 550, UnitOfMeasure: "piece", OnHand: 40},

	// Light Bites & Salads
	{SKU: "SAL-CES-001", Name: "Caesar Salad", Description: "Romaine, croutons, parmesan, caesar dressing", Category: "salads", Price: 500, UnitOfMeasure: "bowl", OnHand: 40},
	{SKU: "SAL-GRK-001", Name: "Greek Salad", Description: "Cucumber, tomato, olives, feta, olive oil", Category: "salads", Price: 450, UnitOfMeasure: "bowl", OnHand: 40},
	{SKU: "BTE-SAM-001", Name: "Samosa (3pc)", Description: "Crispy vegetable samosas with tamarind chutney", Category: "light-bites", Price: 300, UnitOfMeasure: "serving", OnHand: 80},
	{SKU: "BTE-SPR-001", Name: "Spring Rolls (4pc)", Description: "Crispy vegetable spring rolls with sweet chilli sauce", Category: "light-bites", Price: 350, UnitOfMeasure: "serving", OnHand: 60},

	// Breakfast
	{SKU: "BRK-FUL-001", Name: "Full English Breakfast", Description: "Eggs, bacon, sausage, beans, toast, tomato", Category: "breakfast", Price: 800, UnitOfMeasure: "plate", OnHand: 50},
	{SKU: "BRK-PAN-001", Name: "Pancake Stack", Description: "Fluffy pancakes with maple syrup and berries", Category: "breakfast", Price: 550, UnitOfMeasure: "plate", OnHand: 50},
	{SKU: "BRK-AVT-001", Name: "Avocado Toast", Description: "Smashed avocado on sourdough with poached egg", Category: "breakfast", Price: 500, UnitOfMeasure: "plate", OnHand: 50},
	{SKU: "BRK-OAT-001", Name: "Overnight Oats", Description: "Oats soaked in almond milk with fresh fruits and honey", Category: "breakfast", Price: 400, UnitOfMeasure: "bowl", OnHand: 40},
}

func seedItems(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, def := range catalogItems {
		exists, err := client.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(def.SKU)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check item %s: %w", def.SKU, err)
		}
		if exists {
			continue
		}

		_, err = client.Item.Create().
			SetTenantID(tenantID).
			SetSku(def.SKU).
			SetName(def.Name).
			SetDescription(def.Description).
			SetCategory(def.Category).
			SetPrice(def.Price).
			SetUnitOfMeasure(def.UnitOfMeasure).
			SetIsActive(true).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create item %s: %w", def.SKU, err)
		}
		log.Printf("item created: %s — %s", def.SKU, def.Name)
	}
	return nil
}

func seedBalances(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	wh, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.Code("MAIN")).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("find warehouse: %w", err)
	}

	for _, def := range catalogItems {
		itm, err := client.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(def.SKU)).
			Only(ctx)
		if err != nil {
			return fmt.Errorf("find item %s: %w", def.SKU, err)
		}

		_, err = client.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itm.ID).
			SetWarehouseID(wh.ID).
			SetOnHand(def.OnHand).
			SetAvailable(def.OnHand).
			SetReserved(0).
			SetUnitOfMeasure(def.UnitOfMeasure).
			SetUpdatedAt(time.Now()).
			Save(ctx)
		if err != nil {
			// May already exist (unique constraint), skip
			log.Printf("balance for %s: %v (may already exist)", def.SKU, err)
			continue
		}
		log.Printf("balance created: %s on_hand=%d", def.SKU, def.OnHand)
	}
	return nil
}
