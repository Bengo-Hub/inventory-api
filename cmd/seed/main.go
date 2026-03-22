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
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entunit "github.com/bengobox/inventory-service/internal/ent/unit"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
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

	// Resolve tenant UUID and upsert tenant row.
	syncer := tenant.NewSyncer(client)
	// Sync platform org so tenant row exists; platform admin has full access via JWT.
	if _, err := syncer.SyncTenant(ctx, "codevertex"); err != nil {
		log.Printf("[SKIP] sync codevertex (platform org): %v", err)
	}
	tenantID, resolveErr := syncer.SyncTenant(ctx, "urban-loft")
	if resolveErr != nil {
		log.Fatalf("[FATAL] Could not resolve urban-loft UUID from auth-api: %v\nRun auth-api seed before inventory-api seed.", resolveErr)
	}

	log.Printf("seeding with tenant_id = %s (urban-loft)", tenantID)

	if err := seedUnits(ctx, client); err != nil {
		log.Fatalf("seed units: %v", err)
	}

	if err := seedWarehouse(ctx, client, tenantID); err != nil {
		log.Fatalf("seed warehouse: %v", err)
	}

	catIDs, err := seedItemCategories(ctx, client, tenantID)
	if err != nil {
		log.Fatalf("seed item categories: %v", err)
	}

	unitIDs, err := resolveUnitIDs(ctx, client)
	if err != nil {
		log.Fatalf("resolve unit IDs: %v", err)
	}

	if err := seedItems(ctx, client, tenantID, catIDs, unitIDs); err != nil {
		log.Fatalf("seed items: %v", err)
	}

	if err := seedBalances(ctx, client, tenantID); err != nil {
		log.Fatalf("seed balances: %v", err)
	}

	log.Println("seed completed successfully")
}

// ---------------------------------------------------------------------------
// Units — globally shared, no tenant_id
// ---------------------------------------------------------------------------

type unitDef struct {
	Name         string
	Abbreviation string
}

var unitDefs = []unitDef{
	{"PIECE", "pc"},
	{"CUP", "cup"},
	{"SERVING", "srv"},
	{"BOWL", "bowl"},
	{"PLATE", "plate"},
	{"SLICE", "slice"},
	{"KG", "kg"},
	{"GRAM", "g"},
	{"LITRE", "L"},
	{"ML", "ml"},
	{"BOX", "box"},
	{"BOTTLE", "btl"},
	{"SHOT", "shot"},
	{"PACK", "pack"},
	{"BAG", "bag"},
}

func unitUUID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:global:unit:"+name))
}

func seedUnits(ctx context.Context, client *ent.Client) error {
	for _, u := range unitDefs {
		id := unitUUID(u.Name)
		exists, err := client.Unit.Query().Where(entunit.IDEQ(id)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check unit %s: %w", u.Name, err)
		}
		if exists {
			// Update abbreviation in case it changed.
			if _, err := client.Unit.UpdateOneID(id).SetAbbreviation(u.Abbreviation).Save(ctx); err != nil {
				return fmt.Errorf("update unit %s: %w", u.Name, err)
			}
			continue
		}
		if _, err := client.Unit.Create().
			SetID(id).
			SetName(u.Name).
			SetAbbreviation(u.Abbreviation).
			SetIsActive(true).
			Save(ctx); err != nil {
			return fmt.Errorf("create unit %s: %w", u.Name, err)
		}
		log.Printf("unit created: %s", u.Name)
	}
	return nil
}

// resolveUnitIDs returns a map of unit name → uuid.UUID (from DB).
func resolveUnitIDs(ctx context.Context, client *ent.Client) (map[string]uuid.UUID, error) {
	units, err := client.Unit.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query units: %w", err)
	}
	m := make(map[string]uuid.UUID, len(units))
	for _, u := range units {
		m[u.Name] = u.ID
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Warehouse (outlet/branch) — tenant-scoped
// ---------------------------------------------------------------------------

// warehouseUUID returns a deterministic UUID for a warehouse/outlet.
// Uses the same formula as ordering-backend outlet seed so IDs align across services.
func warehouseUUID(tenantSlug, outletSlug string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:cafe:outlet:%s:%s", tenantSlug, outletSlug)))
}

func seedWarehouse(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	// Use deterministic UUID matching ordering-backend's outlet UUID for cross-service alignment.
	whID := warehouseUUID("urban-loft", "busia")

	existing, err := client.Warehouse.Query().
		Where(entwarehouse.ID(whID)).
		Only(ctx)
	if err == nil {
		// Update existing to ensure fields match
		_, _ = client.Warehouse.UpdateOneID(existing.ID).
			SetName("Urban Loft Busia Kitchen").
			SetCode("MAIN").
			SetAddress("Busia, Kenya").
			SetIsDefault(true).
			SetIsActive(true).
			Save(ctx)
		log.Println("warehouse MAIN updated (ID aligned with outlet)")
		return nil
	}
	if !ent.IsNotFound(err) {
		return err
	}

	_, err = client.Warehouse.Create().
		SetID(whID).
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
	log.Printf("warehouse MAIN created (ID=%s, aligned with ordering-backend outlet)", whID)
	return nil
}

// ---------------------------------------------------------------------------
// Item categories — tenant-scoped
// ---------------------------------------------------------------------------

type categoryDef struct {
	Slug        string
	Name        string
	Code        string
	Description string
}

var categoryDefs = []categoryDef{
	{"hot-beverages", "Hot Beverages", "BEV", "Espresso drinks, teas, and other hot beverages"},
	{"cold-beverages", "Cold Beverages", "CBV", "Iced coffees, frappes, smoothies, and fresh juices"},
	{"pastries", "Pastries & Bakery", "PST", "Croissants, muffins, cakes, and baked goods"},
	{"sandwiches", "Sandwiches & Wraps", "SND", "Paninis, wraps, and classic sandwiches"},
	{"salads", "Salads", "SAL", "Fresh salads and greens"},
	{"main-courses", "Main Courses", "MIN", "Grills, curries, rice dishes, and hearty mains"},
	{"light-bites", "Light Bites", "BTE", "Samosas, spring rolls, and quick snacks"},
	{"breakfast", "Breakfast", "BRK", "Full breakfasts, pancakes, oats, and morning meals"},
	{"pizza", "Pizza", "PIZ", "Artisanal and classic pizzas"},
	{"chicken", "Chicken", "CHK", "Fried and grilled chicken specialties"},
	{"sushi", "Sushi", "SHI", "Fresh sushi and Japanese delicacies"},
	{"grocery", "Grocery", "GRC", "Fresh produce and household essentials"},
	{"pharmacy", "Pharmacy", "PHR", "Medication and health services"},
	{"gifts", "Gifts", "GFT", "Special gifts and hampers"},
	{"flowers", "Flowers", "FLW", "Fresh flower bouquets and arrangements"},
	{"alcohol", "Alcohol", "ALC", "Wines, spirits, and beers"},
	{"chinese", "Chinese", "CHN", "Authentic Chinese cuisine"},
	{"indian", "Indian", "IND", "Flavorful Indian curries and specialities"},
	{"desserts", "Desserts", "DST", "Sweet treats and delights"},
	{"retail", "Retail", "RTL", "Shopping and fashion goods"},
	{"electronics", "Electronics", "ELC", "Devices and accessories"},
	{"fashion", "Fashion", "FSH", "Clothing and apparel"},
}

func categoryUUID(tenantID uuid.UUID, slug string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:category:%s:%s", tenantID, slug)))
}

// seedItemCategories upserts all item categories and returns a slug → uuid map.
func seedItemCategories(ctx context.Context, client *ent.Client, tenantID uuid.UUID) (map[string]uuid.UUID, error) {
	catIDs := make(map[string]uuid.UUID, len(categoryDefs))
	for _, cat := range categoryDefs {
		id := categoryUUID(tenantID, cat.Slug)
		catIDs[cat.Slug] = id

		_, err := client.ItemCategory.Get(ctx, id)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.ItemCategory.Create().
				SetID(id).
				SetTenantID(tenantID).
				SetName(cat.Name).
				SetCode(cat.Code).
				SetDescription(cat.Description).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return nil, fmt.Errorf("create category %s: %w", cat.Slug, createErr)
			}
			log.Printf("category created: %s", cat.Name)
		case err != nil:
			return nil, fmt.Errorf("check category %s: %w", cat.Slug, err)
		default:
			// Update name/description/code in case they changed.
			if _, updateErr := client.ItemCategory.UpdateOneID(id).
				SetName(cat.Name).
				SetCode(cat.Code).
				SetDescription(cat.Description).
				Save(ctx); updateErr != nil {
				return nil, fmt.Errorf("update category %s: %w", cat.Slug, updateErr)
			}
		}
	}
	return catIDs, nil
}

// ---------------------------------------------------------------------------
// Items — tenant-scoped, typed, linked to categories and units
// ---------------------------------------------------------------------------

// Media paths — relative to the media server root.
// These match the paths used in ordering-backend and the media folder layout.
const (
	mediaPlaceholder = "/media/images/outlets/menu/placeholder-food.svg"

	imgEspresso     = "/media/images/outlets/menu/espresso.jpg"
	imgCappuccino   = "/media/images/outlets/menu/cappuccino.jpg"
	imgHotCoffee    = "/media/images/outlets/menu/hot coffee.jpeg"
	imgIcedLatte    = "/media/images/outlets/menu/icedlatte.jpeg"
	imgCocktail     = "/media/images/outlets/menu/cocktail.jpeg"
	imgMilkshake    = "/media/images/outlets/menu/milkshake.jpeg"
	imgBurger       = "/media/images/outlets/menu/burger.jpg"
	imgPizza        = "/media/images/outlets/menu/margherita-pizza.jpg"
	imgChicken      = "/media/images/outlets/menu/chicken.jpeg"
	imgChickenUgali = "/media/images/outlets/menu/chicken_ugali.jpeg"
	imgPilau        = "/media/images/outlets/menu/pilau.jpeg"
	imgFish         = "/media/images/outlets/menu/fish.jpeg"
	imgSalad        = "/media/images/outlets/menu/salad.jpg"
	imgBreakfast    = "/media/images/outlets/menu/breakfast.jpg"
	imgOats         = "/media/images/outlets/menu/oats.jpeg"
	imgDessert      = "/media/images/outlets/menu/dessert.jpeg"
	imgLavaCake     = "/media/images/outlets/menu/chocolate-lava-cake.jpg"
	imgMain1        = "/media/images/outlets/menu/main-course-1.jpg"
	imgMain2        = "/media/images/outlets/menu/main-course-2.jpg"
)

type itemDef struct {
	SKU          string
	Name         string
	Description  string
	CategorySlug string
	ItemType     entitem.Type
	UnitName     string // matches Unit.Name (e.g. "CUP", "PIECE")
	ImageURL     string
	OnHand       int
}

// catalogItemDefs is the authoritative list of Urban Loft café items.
// Inventory-api is the single source of truth; ordering-backend and pos-api project from here.
var catalogItemDefs = []itemDef{
	// ── Hot Beverages ─────────────────────────────────────────────────────────
	{"BEV-ESP-001", "Espresso", "Single shot of rich espresso", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 500},
	{"BEV-ESP-002", "Double Espresso", "Double shot espresso", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 500},
	{"BEV-LAT-001", "Caffe Latte", "Espresso with steamed milk", "hot-beverages", entitem.TypeRECIPE, "CUP", imgCappuccino, 400},
	{"BEV-CAP-001", "Cappuccino", "Espresso with frothed milk and cocoa", "hot-beverages", entitem.TypeRECIPE, "CUP", imgCappuccino, 400},
	{"BEV-AME-001", "Americano", "Espresso with hot water", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 500},
	{"BEV-MOC-001", "Mocha", "Espresso, chocolate, steamed milk, whipped cream", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 300},
	{"BEV-MAC-001", "Macchiato", "Espresso with a dash of milk foam", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 400},
	{"BEV-TEA-001", "Kenya AA Black Tea", "Premium Kenyan black tea", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 600},
	{"BEV-TEA-002", "Masala Chai", "Spiced tea latte with cardamom and ginger", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 400},
	{"BEV-HOT-001", "Hot Chocolate", "Rich hot chocolate with whipped cream", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 300},

	// ── Cold Beverages ────────────────────────────────────────────────────────
	{"BEV-ICE-001", "Iced Latte", "Chilled espresso with cold milk over ice", "cold-beverages", entitem.TypeRECIPE, "CUP", imgIcedLatte, 300},
	{"BEV-ICE-002", "Iced Americano", "Espresso over ice with cold water", "cold-beverages", entitem.TypeRECIPE, "CUP", imgIcedLatte, 300},
	{"BEV-FRP-001", "Caramel Frappe", "Blended iced coffee with caramel drizzle", "cold-beverages", entitem.TypeRECIPE, "CUP", imgMilkshake, 200},
	{"BEV-FRP-002", "Vanilla Frappe", "Blended iced coffee with vanilla", "cold-beverages", entitem.TypeRECIPE, "CUP", imgMilkshake, 200},
	{"BEV-SMO-001", "Mango Smoothie", "Fresh mango blended with yoghurt", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 150},
	{"BEV-SMO-002", "Mixed Berry Smoothie", "Strawberry, blueberry, and banana blend", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 150},
	{"BEV-JCE-001", "Fresh Orange Juice", "Freshly squeezed orange juice", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 200},

	// ── Pastries & Bakery ─────────────────────────────────────────────────────
	{"PST-CRO-001", "Butter Croissant", "Flaky French butter croissant", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 100},
	{"PST-CRO-002", "Chocolate Croissant", "Croissant filled with dark chocolate", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80},
	{"PST-MUF-001", "Blueberry Muffin", "Moist muffin loaded with blueberries", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80},
	{"PST-MUF-002", "Banana Walnut Muffin", "Banana muffin with crunchy walnuts", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80},
	{"PST-CKE-001", "Carrot Cake Slice", "Spiced carrot cake with cream cheese frosting", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 50},
	{"PST-CKE-002", "Red Velvet Cake Slice", "Classic red velvet with vanilla cream cheese", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 40},
	{"PST-CKE-003", "Chocolate Fudge Cake Slice", "Rich chocolate fudge layer cake", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 40},
	{"PST-DAN-001", "Danish Pastry", "Flaky pastry with custard and fruit", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 60},
	{"PST-SCO-001", "Classic Scone", "Buttermilk scone with clotted cream and jam", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 70},

	// ── Sandwiches & Wraps ────────────────────────────────────────────────────
	{"SND-CLB-001", "Club Sandwich", "Triple-decker with chicken, bacon, lettuce, tomato", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 60},
	{"SND-GRL-001", "Grilled Chicken Panini", "Grilled chicken, pesto, mozzarella on ciabatta", "sandwiches", entitem.TypeRECIPE, "PIECE", imgChicken, 50},
	{"SND-VEG-001", "Veggie Wrap", "Hummus, avocado, roasted vegetables in tortilla", "sandwiches", entitem.TypeRECIPE, "PIECE", imgSalad, 50},
	{"SND-BLT-001", "BLT Sandwich", "Bacon, lettuce, tomato on toasted sourdough", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 50},
	{"SND-TUN-001", "Tuna Melt", "Tuna salad with melted cheddar on rye bread", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 40},

	// ── Salads ────────────────────────────────────────────────────────────────
	{"SAL-CES-001", "Caesar Salad", "Romaine, croutons, parmesan, caesar dressing", "salads", entitem.TypeRECIPE, "BOWL", imgSalad, 40},
	{"SAL-GRK-001", "Greek Salad", "Cucumber, tomato, olives, feta, olive oil", "salads", entitem.TypeRECIPE, "BOWL", imgSalad, 40},

	// ── Light Bites ───────────────────────────────────────────────────────────
	{"BTE-SAM-001", "Samosa (3pc)", "Crispy vegetable samosas with tamarind chutney", "light-bites", entitem.TypeRECIPE, "SERVING", imgMain2, 80},
	{"BTE-SPR-001", "Spring Rolls (4pc)", "Crispy vegetable spring rolls with sweet chilli sauce", "light-bites", entitem.TypeRECIPE, "SERVING", imgMain2, 60},

	// ── Main Courses ──────────────────────────────────────────────────────────
	{"MIN-GRL-001", "Grilled Beef Fillet", "250g beef fillet with pepper sauce, mash and seasonal veg", "main-courses", entitem.TypeRECIPE, "PLATE", imgMain1, 30},
	{"MIN-GRL-002", "Grilled Chicken Breast", "Herb-marinated chicken with gravy, rice and vegetables", "main-courses", entitem.TypeRECIPE, "PLATE", imgChickenUgali, 40},
	{"MIN-CUR-001", "Chicken Curry", "Spiced chicken curry with basmati rice and naan", "main-courses", entitem.TypeRECIPE, "PLATE", imgChicken, 40},
	{"MIN-CUR-002", "Beef Stew", "Tender beef stew with potatoes and carrots, served with ugali or rice", "main-courses", entitem.TypeRECIPE, "PLATE", imgPilau, 40},
	{"MIN-SEA-001", "Fish and Chips", "Beer-battered fish with chips and tartar sauce", "main-courses", entitem.TypeRECIPE, "PLATE", imgFish, 30},
	{"MIN-PAS-001", "Spaghetti Bolognese", "Classic beef bolognese with parmesan and garlic bread", "main-courses", entitem.TypeRECIPE, "PLATE", imgMain1, 30},
	{"MIN-RIC-001", "Pilau Rice Bowl", "Spiced pilau rice with choice of beef, chicken or veg", "main-courses", entitem.TypeRECIPE, "BOWL", imgPilau, 50},

	// ── Breakfast ─────────────────────────────────────────────────────────────
	{"BRK-FUL-001", "Full English Breakfast", "Eggs, bacon, sausage, beans, toast, tomato", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50},
	{"BRK-PAN-001", "Pancake Stack", "Fluffy pancakes with maple syrup and berries", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50},
	{"BRK-AVT-001", "Avocado Toast", "Smashed avocado on sourdough with poached egg", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50},
	{"BRK-OAT-001", "Overnight Oats", "Oats soaked in almond milk with fresh fruits and honey", "breakfast", entitem.TypeRECIPE, "BOWL", imgOats, 40},

	// ── Pizza ─────────────────────────────────────────────────────────────────
	{"PIZ-MAR-001", "Margherita Pizza", "Fresh mozzarella, tomato sauce, and basil", "pizza", entitem.TypeRECIPE, "PIECE", imgPizza, 30},
	{"PIZ-PEP-001", "Pepperoni Pizza", "Classic pepperoni with mozzarella and tomato sauce", "pizza", entitem.TypeRECIPE, "PIECE", imgPizza, 30},
}

func itemUUID(tenantID uuid.UUID, sku string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:item:%s:%s", tenantID, sku)))
}

func itemAssetUUID(itemID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:itemasset:primary:%s", itemID)))
}

func seedItems(ctx context.Context, client *ent.Client, tenantID uuid.UUID, catIDs map[string]uuid.UUID, unitIDs map[string]uuid.UUID) error {
	for _, def := range catalogItemDefs {
		id := itemUUID(tenantID, def.SKU)

		catID, ok := catIDs[def.CategorySlug]
		if !ok {
			return fmt.Errorf("unknown category slug %q for SKU %s", def.CategorySlug, def.SKU)
		}

		unitID, ok := unitIDs[def.UnitName]
		if !ok {
			return fmt.Errorf("unknown unit %q for SKU %s — ensure seedUnits ran first", def.UnitName, def.SKU)
		}

		imgURL := def.ImageURL
		if imgURL == "" {
			imgURL = mediaPlaceholder
		}

		_, err := client.Item.Get(ctx, id)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.Item.Create().
				SetID(id).
				SetTenantID(tenantID).
				SetSku(def.SKU).
				SetName(def.Name).
				SetDescription(def.Description).
				SetCategoryID(catID).
				SetUnitID(unitID).
				SetType(def.ItemType).
				SetImageURL(imgURL).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return fmt.Errorf("create item %s: %w", def.SKU, createErr)
			}
			log.Printf("item created: %s — %s", def.SKU, def.Name)
		case err != nil:
			return fmt.Errorf("check item %s: %w", def.SKU, err)
		default:
			// Update mutable fields: name, description, category, unit, type, image.
			if _, updateErr := client.Item.UpdateOneID(id).
				SetName(def.Name).
				SetDescription(def.Description).
				SetCategoryID(catID).
				SetUnitID(unitID).
				SetType(def.ItemType).
				SetImageURL(imgURL).
				Save(ctx); updateErr != nil {
				return fmt.Errorf("update item %s: %w", def.SKU, updateErr)
			}
		}

		// Upsert primary image asset.
		if err := upsertPrimaryAsset(ctx, client, id, imgURL); err != nil {
			return fmt.Errorf("upsert asset for %s: %w", def.SKU, err)
		}
	}
	return nil
}

// upsertPrimaryAsset creates or updates the primary IMAGE asset for an item.
func upsertPrimaryAsset(ctx context.Context, client *ent.Client, itemID uuid.UUID, imgURL string) error {
	assetID := itemAssetUUID(itemID)

	existing, err := client.ItemAsset.Query().
		Where(itemasset.ItemID(itemID), itemasset.IsPrimary(true)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query primary asset: %w", err)
	}

	if existing != nil {
		// Update URL if it changed (media path may be updated after re-seed).
		if existing.URL != imgURL {
			if _, err := client.ItemAsset.UpdateOneID(existing.ID).SetURL(imgURL).Save(ctx); err != nil {
				return fmt.Errorf("update asset URL: %w", err)
			}
		}
		return nil
	}

	// Create primary asset.
	if _, err := client.ItemAsset.Create().
		SetID(assetID).
		SetItemID(itemID).
		SetAssetType("IMAGE").
		SetURL(imgURL).
		SetIsPrimary(true).
		SetDisplayOrder(0).
		SetMimeType(mimeFromURL(imgURL)).
		Save(ctx); err != nil {
		return fmt.Errorf("create primary asset: %w", err)
	}
	return nil
}

// mimeFromURL returns a best-effort MIME type from the file extension.
func mimeFromURL(url string) string {
	switch {
	case len(url) >= 4 && url[len(url)-4:] == ".jpg":
		return "image/jpeg"
	case len(url) >= 5 && url[len(url)-5:] == ".jpeg":
		return "image/jpeg"
	case len(url) >= 4 && url[len(url)-4:] == ".png":
		return "image/png"
	case len(url) >= 4 && url[len(url)-4:] == ".svg":
		return "image/svg+xml"
	case len(url) >= 5 && url[len(url)-5:] == ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// ---------------------------------------------------------------------------
// Inventory balances — per item per warehouse
// ---------------------------------------------------------------------------

func seedBalances(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	wh, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.Code("MAIN")).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("find warehouse: %w", err)
	}

	for _, def := range catalogItemDefs {
		id := itemUUID(tenantID, def.SKU)

		itm, err := client.Item.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("find item %s: %w", def.SKU, err)
		}

		// Check if balance already exists (unique: tenant_id, item_id, warehouse_id).
		exists, err := client.InventoryBalance.Query().
			Where(
				entinvbal.TenantID(tenantID),
				entinvbal.ItemID(itm.ID),
				entinvbal.WarehouseID(wh.ID),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check balance for %s: %w", def.SKU, err)
		}
		if exists {
			continue
		}

		if _, err := client.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itm.ID).
			SetWarehouseID(wh.ID).
			SetOnHand(def.OnHand).
			SetAvailable(def.OnHand).
			SetReserved(0).
			SetReorderLevel(1).
			SetUnitOfMeasure(def.UnitName).
			SetUpdatedAt(time.Now()).
			Save(ctx); err != nil {
			log.Printf("balance for %s: %v (may already exist)", def.SKU, err)
			continue
		}
		log.Printf("balance created: %s on_hand=%d", def.SKU, def.OnHand)
	}
	return nil
}
