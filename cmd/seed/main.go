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
	entinvperm "github.com/bengobox/inventory-service/internal/ent/inventorypermission"
	entinvrole "github.com/bengobox/inventory-service/internal/ent/inventoryrole"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entrlc "github.com/bengobox/inventory-service/internal/ent/ratelimitconfig"
	entrp "github.com/bengobox/inventory-service/internal/ent/rolepermission"
	entsvc "github.com/bengobox/inventory-service/internal/ent/serviceconfig"
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
	authURL := os.Getenv("AUTH_API_URL")
	if authURL == "" {
		authURL = "https://sso.codevertexitsolutions.com"
	}
	syncer := tenant.NewSyncer(client, authURL)
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

	// RBAC seed — permissions, roles, role-permission assignments
	if err := seedPermissions(ctx, client); err != nil {
		log.Fatalf("seed permissions: %v", err)
	}
	if err := seedRoles(ctx, client, tenantID); err != nil {
		log.Fatalf("seed roles: %v", err)
	}
	if err := seedRolePermissions(ctx, client, tenantID); err != nil {
		log.Fatalf("seed role-permissions: %v", err)
	}
	if err := seedRecipes(ctx, client, tenantID); err != nil {
		log.Fatalf("seed recipes: %v", err)
	}

	if err := seedRateLimitConfigs(ctx, client); err != nil {
		log.Fatalf("seed rate limit configs: %v", err)
	}
	if err := seedServiceConfigs(ctx, client); err != nil {
		log.Fatalf("seed service configs: %v", err)
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
	Icon        string
}

var categoryDefs = []categoryDef{
	{"hot-beverages", "Hot Beverages", "BEV", "Espresso drinks, teas, and other hot beverages", "/media/icons/coffee-colored.svg"},
	{"cold-beverages", "Cold Beverages", "CBV", "Iced coffees, frappes, smoothies, and fresh juices", "/media/icons/juice-colored.svg"},
	{"pastries", "Pastries & Bakery", "PST", "Croissants, muffins, cakes, and baked goods", "/media/icons/cake-colored.svg"},
	{"sandwiches", "Sandwiches & Wraps", "SND", "Paninis, wraps, and classic sandwiches", "/media/icons/sandwich-colored.svg"},
	{"salads", "Salads", "SAL", "Fresh salads and greens", "/media/icons/fresh-colored.svg"},
	{"main-courses", "Main Courses", "MIN", "Grills, curries, rice dishes, and hearty mains", "/media/icons/burger-colored.svg"},
	{"light-bites", "Light Bites", "BTE", "Samosas, spring rolls, and quick snacks", "/media/icons/snack-colored.svg"},
	{"breakfast", "Breakfast", "BRK", "Full breakfasts, pancakes, oats, and morning meals", "/media/icons/breakfast-colored.svg"},
	{"pizza", "Pizza", "PIZ", "Artisanal and classic pizzas", "/media/icons/pizza-colored.svg"},
	{"chicken", "Chicken", "CHK", "Fried and grilled chicken specialties", "/media/icons/drumstick-colored.svg"},
	{"sushi", "Sushi", "SHI", "Fresh sushi and Japanese delicacies", "/media/icons/sushi-colored.svg"},
	{"grocery", "Grocery", "GRC", "Fresh produce and household essentials", "/media/icons/grocery-colored.svg"},
	{"pharmacy", "Pharmacy", "PHR", "Medication and health services", "/media/icons/medicine-colored.svg"},
	{"gifts", "Gifts", "GFT", "Special gifts and hampers", "/media/icons/gift-colored.svg"},
	{"flowers", "Flowers", "FLW", "Fresh flower bouquets and arrangements", "/media/icons/flower-colored.svg"},
	{"alcohol", "Alcohol", "ALC", "Wines, spirits, and beers", "/media/icons/liquor-colored.svg"},
	{"chinese", "Chinese", "CHN", "Authentic Chinese cuisine", "/media/icons/chinese-colored.svg"},
	{"indian", "Indian", "IND", "Flavorful Indian curries and specialities", "/media/icons/curry-colored.svg"},
	{"desserts", "Desserts", "DST", "Sweet treats and delights", "/media/icons/dessert-colored.svg"},
	{"retail", "Retail", "RTL", "Shopping and fashion goods", "/media/icons/retail-colored.svg"},
	{"electronics", "Electronics", "ELC", "Devices and accessories", "/media/icons/electronics-colored.svg"},
	{"fashion", "Fashion", "FSH", "Clothing and apparel", "/media/icons/fashion-colored.svg"},
	{"fresh", "Fresh", "FRS", "Fresh fruits, vegetables, and produce", "/media/icons/fresh-colored.svg"},
	{"juice", "Juice", "JCE", "Fresh juices and smoothies", "/media/icons/juice-colored.svg"},
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
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetIcon(cat.Icon).
				SetDescription(cat.Description).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return nil, fmt.Errorf("create category %s: %w", cat.Slug, createErr)
			}
			log.Printf("category created: %s", cat.Name)
		case err != nil:
			return nil, fmt.Errorf("check category %s: %w", cat.Slug, err)
		default:
			// Update name/description/code/icon/slug in case they changed.
			if _, updateErr := client.ItemCategory.UpdateOneID(id).
				SetName(cat.Name).
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetIcon(cat.Icon).
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

	// ── Raw Ingredients (GOODS) ───────────────────────────────────────────────
	{"RAW-ESP-001", "Espresso Beans", "Premium roasted espresso beans", "hot-beverages", entitem.TypeGOODS, "KG", "", 50},
	{"RAW-MLK-001", "Fresh Milk", "Full-cream fresh milk", "hot-beverages", entitem.TypeGOODS, "LITRE", "", 100},
	{"RAW-CHO-001", "Chocolate Syrup", "Dark chocolate syrup", "hot-beverages", entitem.TypeGOODS, "LITRE", "", 20},
	{"RAW-SGR-001", "Sugar", "White granulated sugar", "hot-beverages", entitem.TypeGOODS, "KG", "", 50},
	{"RAW-VAN-001", "Vanilla Syrup", "Vanilla flavoured syrup", "cold-beverages", entitem.TypeGOODS, "LITRE", "", 15},
	{"RAW-CAR-001", "Caramel Syrup", "Caramel flavoured syrup", "cold-beverages", entitem.TypeGOODS, "LITRE", "", 15},
	{"RAW-ICE-001", "Ice Cubes", "Filtered water ice cubes", "cold-beverages", entitem.TypeGOODS, "KG", "", 200},
	{"RAW-CRM-001", "Whipped Cream", "Ready whipped cream", "hot-beverages", entitem.TypeGOODS, "LITRE", "", 20},
	{"RAW-COC-001", "Cocoa Powder", "Dutch-process cocoa powder", "hot-beverages", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-TEA-001", "Kenya AA Tea Leaves", "Premium Kenyan black tea leaves", "hot-beverages", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-GNG-001", "Ginger", "Fresh ginger root", "hot-beverages", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-CDM-001", "Cardamom", "Ground cardamom spice", "hot-beverages", entitem.TypeGOODS, "KG", "", 5},
	{"RAW-MNG-001", "Fresh Mango", "Ripe mangoes", "cold-beverages", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-YGT-001", "Yoghurt", "Plain natural yoghurt", "cold-beverages", entitem.TypeGOODS, "LITRE", "", 20},
	{"RAW-BRY-001", "Mixed Berries", "Frozen mixed berries (strawberry, blueberry)", "cold-beverages", entitem.TypeGOODS, "KG", "", 15},
	{"RAW-BNA-001", "Bananas", "Fresh bananas", "cold-beverages", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-ORG-001", "Fresh Oranges", "Navel oranges for juicing", "cold-beverages", entitem.TypeGOODS, "KG", "", 40},
	{"RAW-BRD-001", "Sourdough Bread", "Artisan sourdough loaf", "sandwiches", entitem.TypeGOODS, "PIECE", "", 30},
	{"RAW-CKN-001", "Chicken Breast", "Fresh boneless chicken breast", "main-courses", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-BCN-001", "Bacon", "Smoked streaky bacon", "sandwiches", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-LET-001", "Lettuce", "Fresh iceberg and romaine lettuce", "salads", entitem.TypeGOODS, "KG", "", 25},
	{"RAW-TMT-001", "Tomatoes", "Fresh vine tomatoes", "salads", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-CHZ-001", "Mozzarella Cheese", "Fresh mozzarella", "pizza", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-PRM-001", "Parmesan Cheese", "Aged parmesan block", "salads", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-PST-001", "Pesto Sauce", "Basil pesto", "sandwiches", entitem.TypeGOODS, "LITRE", "", 10},
	{"RAW-TRT-001", "Tortilla Wraps", "Large flour tortilla wraps", "sandwiches", entitem.TypeGOODS, "PIECE", "", 50},
	{"RAW-HUM-001", "Hummus", "Classic chickpea hummus", "sandwiches", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-AVO-001", "Avocado", "Fresh ripe avocados", "salads", entitem.TypeGOODS, "PIECE", "", 40},
	{"RAW-TNA-001", "Canned Tuna", "Tuna chunks in brine", "sandwiches", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-CDD-001", "Cheddar Cheese", "Mature cheddar slices", "sandwiches", entitem.TypeGOODS, "KG", "", 15},
	{"RAW-RYE-001", "Rye Bread", "Dark rye bread loaf", "sandwiches", entitem.TypeGOODS, "PIECE", "", 20},
	{"RAW-OLV-001", "Olives", "Kalamata olives", "salads", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-FTA-001", "Feta Cheese", "Greek feta cheese", "salads", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-CUC-001", "Cucumber", "Fresh cucumbers", "salads", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-OVO-001", "Olive Oil", "Extra virgin olive oil", "salads", entitem.TypeGOODS, "LITRE", "", 15},
	{"RAW-CRT-001", "Croutons", "Seasoned croutons", "salads", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-CSR-001", "Caesar Dressing", "Classic caesar dressing", "salads", entitem.TypeGOODS, "LITRE", "", 10},
	{"RAW-SAM-001", "Samosa Wraps", "Pre-made samosa pastry wraps", "light-bites", entitem.TypeGOODS, "PIECE", "", 100},
	{"RAW-SPW-001", "Spring Roll Wraps", "Rice paper spring roll wraps", "light-bites", entitem.TypeGOODS, "PIECE", "", 100},
	{"RAW-BEF-001", "Beef Fillet", "Prime beef fillet steaks", "main-courses", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-RIC-001", "Basmati Rice", "Long-grain basmati rice", "main-courses", entitem.TypeGOODS, "KG", "", 50},
	{"RAW-NAN-001", "Naan Bread", "Fresh tandoori naan", "main-courses", entitem.TypeGOODS, "PIECE", "", 40},
	{"RAW-POT-001", "Potatoes", "Fresh white potatoes", "main-courses", entitem.TypeGOODS, "KG", "", 50},
	{"RAW-FSH-001", "White Fish Fillet", "Beer-batter-ready cod/tilapia fillets", "main-courses", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-SPG-001", "Spaghetti", "Dried spaghetti pasta", "main-courses", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-MNC-001", "Minced Beef", "Lean minced beef", "main-courses", entitem.TypeGOODS, "KG", "", 25},
	{"RAW-PIL-001", "Pilau Spice Mix", "Traditional pilau masala", "main-courses", entitem.TypeGOODS, "KG", "", 5},
	{"RAW-EGG-001", "Eggs", "Free-range eggs", "breakfast", entitem.TypeGOODS, "PIECE", "", 200},
	{"RAW-SSG-001", "Pork Sausages", "Breakfast pork sausages", "breakfast", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-BNS-001", "Baked Beans", "Tinned baked beans", "breakfast", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-PNC-001", "Pancake Mix", "Ready-mix pancake batter", "breakfast", entitem.TypeGOODS, "KG", "", 15},
	{"RAW-MPS-001", "Maple Syrup", "Canadian maple syrup", "breakfast", entitem.TypeGOODS, "LITRE", "", 10},
	{"RAW-OAT-001", "Rolled Oats", "Organic rolled oats", "breakfast", entitem.TypeGOODS, "KG", "", 20},
	{"RAW-ALM-001", "Almond Milk", "Unsweetened almond milk", "breakfast", entitem.TypeGOODS, "LITRE", "", 15},
	{"RAW-HNY-001", "Honey", "Raw natural honey", "breakfast", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-TMC-001", "Tomato Sauce", "Pizza and pasta tomato sauce", "pizza", entitem.TypeGOODS, "LITRE", "", 20},
	{"RAW-BSL-001", "Fresh Basil", "Fresh basil leaves", "pizza", entitem.TypeGOODS, "KG", "", 5},
	{"RAW-PEP-001", "Pepperoni", "Sliced pepperoni", "pizza", entitem.TypeGOODS, "KG", "", 10},
	{"RAW-PZD-001", "Pizza Dough", "Fresh pizza dough balls", "pizza", entitem.TypeGOODS, "PIECE", "", 40},
	{"RAW-FLR-001", "Wheat Flour", "All-purpose wheat flour", "pastries", entitem.TypeGOODS, "KG", "", 50},
	{"RAW-BTR-001", "Butter", "Unsalted butter blocks", "pastries", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-CRY-001", "Curry Powder", "Blended curry powder", "main-courses", entitem.TypeGOODS, "KG", "", 5},
	{"RAW-UGL-001", "Ugali Flour", "White maize flour for ugali", "main-courses", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-TAR-001", "Tartar Sauce", "Ready tartar sauce", "main-courses", entitem.TypeGOODS, "LITRE", "", 10},
	{"RAW-VEG-001", "Mixed Vegetables", "Seasonal mixed vegetables", "main-courses", entitem.TypeGOODS, "KG", "", 30},
	{"RAW-TMR-001", "Tamarind Chutney", "Tangy tamarind chutney", "light-bites", entitem.TypeGOODS, "LITRE", "", 5},
	{"RAW-SCS-001", "Sweet Chilli Sauce", "Thai sweet chilli dipping sauce", "light-bites", entitem.TypeGOODS, "LITRE", "", 10},
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

// ---------------------------------------------------------------------------
// RBAC — Permissions (global, no tenant_id)
// ---------------------------------------------------------------------------

type permDef struct {
	Code        string
	Name        string
	Module      string
	Action      string
	Description string
}

func permUUID(code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:permission:"+code))
}

// buildPermDefs generates the full set of inventory permissions.
func buildPermDefs() []permDef {
	modules := []struct {
		module string
		label  string
		note   string // additional description context
	}{
		{"items", "Items", "catalog/SKU management"},
		{"variants", "Variants", "item variant management"},
		{"categories", "Categories", "item category management"},
		{"warehouses", "Warehouses", "warehouse and location management"},
		{"stock", "Stock", "stock adjustments, cycle counts, transfers"},
		{"recipes", "Recipes", "recipe/BOM management"},
		{"consumptions", "Consumptions", "stock consumption tracking"},
		{"reservations", "Reservations", "inventory reservations and allocation"},
		{"units", "Units", "unit of measure management (platform-only for manage operations)"},
		{"config", "Config", "service configuration management"},
		{"users", "Users", "user management"},
	}

	actions := []struct {
		action string
		verb   string
	}{
		{"add", "Add"},
		{"view", "View"},
		{"view_own", "View own"},
		{"change", "Change"},
		{"change_own", "Change own"},
		{"delete", "Delete"},
		{"delete_own", "Delete own"},
		{"manage", "Manage"},
		{"manage_own", "Manage own"},
	}

	var defs []permDef
	for _, m := range modules {
		for _, a := range actions {
			code := fmt.Sprintf("inventory.%s.%s", m.module, a.action)
			name := fmt.Sprintf("%s %s", a.verb, m.label)
			desc := fmt.Sprintf("%s — %s", name, m.note)
			defs = append(defs, permDef{
				Code:        code,
				Name:        name,
				Module:      m.module,
				Action:      a.action,
				Description: desc,
			})
		}
	}
	return defs
}

func seedPermissions(ctx context.Context, client *ent.Client) error {
	defs := buildPermDefs()
	for _, d := range defs {
		id := permUUID(d.Code)
		exists, err := client.InventoryPermission.Query().
			Where(entinvperm.PermissionCode(d.Code)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check permission %s: %w", d.Code, err)
		}
		if exists {
			continue
		}
		if _, err := client.InventoryPermission.Create().
			SetID(id).
			SetPermissionCode(d.Code).
			SetName(d.Name).
			SetModule(d.Module).
			SetAction(d.Action).
			SetResource(d.Module).
			SetDescription(d.Description).
			Save(ctx); err != nil {
			return fmt.Errorf("create permission %s: %w", d.Code, err)
		}
		log.Printf("permission created: %s", d.Code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// RBAC — Roles (tenant-scoped)
// ---------------------------------------------------------------------------

type roleDef struct {
	Code        string
	Name        string
	Description string
	IsSystem    bool
}

func roleUUID(tenantID uuid.UUID, code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:role:%s:%s", tenantID, code)))
}

var roleDefs = []roleDef{
	{"inventory_admin", "Inventory Admin", "Full access to all inventory operations including config and user management", true},
	{"warehouse_manager", "Warehouse Manager", "Manage warehouses, stock, reservations, recipes, and consumptions", true},
	{"stock_clerk", "Stock Clerk", "View and change stock, view items and warehouses, manage own consumptions", true},
	{"viewer", "Viewer", "Read-only access to all inventory data", true},
}

func seedRoles(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, d := range roleDefs {
		id := roleUUID(tenantID, d.Code)
		exists, err := client.InventoryRole.Query().
			Where(entinvrole.TenantID(tenantID), entinvrole.RoleCode(d.Code)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check role %s: %w", d.Code, err)
		}
		if exists {
			continue
		}
		if _, err := client.InventoryRole.Create().
			SetID(id).
			SetTenantID(tenantID).
			SetRoleCode(d.Code).
			SetName(d.Name).
			SetDescription(d.Description).
			SetIsSystemRole(d.IsSystem).
			Save(ctx); err != nil {
			return fmt.Errorf("create role %s: %w", d.Code, err)
		}
		log.Printf("role created: %s", d.Code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// RBAC — Role-Permission assignments
// ---------------------------------------------------------------------------

// rolePermMap defines which permission codes each role gets.
var rolePermMap = map[string][]string{
	"inventory_admin": nil, // populated below — gets ALL permissions
	"warehouse_manager": {
		// warehouses — full
		"inventory.warehouses.add", "inventory.warehouses.view", "inventory.warehouses.change", "inventory.warehouses.delete", "inventory.warehouses.manage",
		// stock — full
		"inventory.stock.add", "inventory.stock.view", "inventory.stock.change", "inventory.stock.delete", "inventory.stock.manage",
		// items — view + change
		"inventory.items.view", "inventory.items.change", "inventory.items.add",
		// variants — view + change
		"inventory.variants.view", "inventory.variants.change", "inventory.variants.add",
		// categories — view + change
		"inventory.categories.view", "inventory.categories.change", "inventory.categories.add",
		// recipes — full
		"inventory.recipes.add", "inventory.recipes.view", "inventory.recipes.change", "inventory.recipes.delete", "inventory.recipes.manage",
		// consumptions — full
		"inventory.consumptions.add", "inventory.consumptions.view", "inventory.consumptions.change", "inventory.consumptions.delete", "inventory.consumptions.manage",
		// reservations — full
		"inventory.reservations.add", "inventory.reservations.view", "inventory.reservations.change", "inventory.reservations.delete", "inventory.reservations.manage",
		// units — view only (units are global/platform-only for manage)
		"inventory.units.view",
	},
	"stock_clerk": {
		// stock — view + change + add
		"inventory.stock.view", "inventory.stock.change", "inventory.stock.add",
		// items — view
		"inventory.items.view",
		// variants — view
		"inventory.variants.view",
		// categories — view
		"inventory.categories.view",
		// warehouses — view
		"inventory.warehouses.view",
		// consumptions — own
		"inventory.consumptions.view_own", "inventory.consumptions.add", "inventory.consumptions.change_own", "inventory.consumptions.manage_own",
		// reservations — view
		"inventory.reservations.view",
		// units — view
		"inventory.units.view",
		// recipes — view
		"inventory.recipes.view",
	},
	"viewer": {
		"inventory.items.view",
		"inventory.variants.view",
		"inventory.categories.view",
		"inventory.warehouses.view",
		"inventory.stock.view",
		"inventory.recipes.view",
		"inventory.consumptions.view",
		"inventory.reservations.view",
		"inventory.units.view",
	},
}

func seedRolePermissions(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	// Build all permission codes for admin
	allPerms := buildPermDefs()
	adminCodes := make([]string, 0, len(allPerms))
	for _, p := range allPerms {
		adminCodes = append(adminCodes, p.Code)
	}
	rolePermMap["inventory_admin"] = adminCodes

	for _, rd := range roleDefs {
		roleID := roleUUID(tenantID, rd.Code)
		permCodes, ok := rolePermMap[rd.Code]
		if !ok {
			continue
		}
		for _, code := range permCodes {
			permID := permUUID(code)
			exists, err := client.RolePermission.Query().
				Where(entrp.RoleID(roleID), entrp.PermissionID(permID)).
				Exist(ctx)
			if err != nil {
				return fmt.Errorf("check role-perm %s/%s: %w", rd.Code, code, err)
			}
			if exists {
				continue
			}
			if _, err := client.RolePermission.Create().
				SetRoleID(roleID).
				SetPermissionID(permID).
				Save(ctx); err != nil {
				return fmt.Errorf("assign perm %s to role %s: %w", code, rd.Code, err)
			}
		}
		log.Printf("role-permissions assigned: %s (%d perms)", rd.Code, len(permCodes))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Rate Limit Configs
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
}

func seedServiceConfigs(ctx context.Context, client *ent.Client) error {
	for _, d := range serviceConfigDefs {
		id := serviceConfigUUID(d.Key)
		exists, err := client.ServiceConfig.Query().
			Where(
				entsvc.ConfigKey(d.Key),
				entsvc.TenantIDIsNil(), // platform-level
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

// ---------------------------------------------------------------------------
// Recipes — BOM linking menu items (RECIPE type) to raw ingredient items
// ---------------------------------------------------------------------------

type ingredientDef struct {
	RawSKU string
	Qty    float64
	UOM    string
}

type recipeDef struct {
	SKU         string // matches a RECIPE-type catalogItemDef.SKU
	OutputQty   float64
	UOM         string
	Ingredients []ingredientDef
}

func recipeUUID(tenantID uuid.UUID, sku string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:recipe:%s:%s", tenantID, sku)))
}

func recipeIngredientUUID(recipeID uuid.UUID, rawSKU string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:recipe-ing:%s:%s", recipeID, rawSKU)))
}

// recipeDefs maps every RECIPE-type menu item to its raw ingredient BOM.
var recipeDefs = []recipeDef{
	// ── Hot Beverages ─────────────────────────────────────────────────────
	{"BEV-ESP-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}}},
	{"BEV-ESP-002", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.036, "KG"}}},
	{"BEV-LAT-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.25, "LITRE"}}},
	{"BEV-CAP-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.2, "LITRE"}, {"RAW-COC-001", 0.005, "KG"}}},
	{"BEV-AME-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}}},
	{"BEV-MOC-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.2, "LITRE"}, {"RAW-CHO-001", 0.03, "LITRE"}, {"RAW-CRM-001", 0.03, "LITRE"}}},
	{"BEV-MAC-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.05, "LITRE"}}},
	{"BEV-TEA-001", 1, "CUP", []ingredientDef{{"RAW-TEA-001", 0.005, "KG"}, {"RAW-SGR-001", 0.01, "KG"}}},
	{"BEV-TEA-002", 1, "CUP", []ingredientDef{{"RAW-TEA-001", 0.005, "KG"}, {"RAW-MLK-001", 0.15, "LITRE"}, {"RAW-GNG-001", 0.005, "KG"}, {"RAW-CDM-001", 0.002, "KG"}, {"RAW-SGR-001", 0.01, "KG"}}},
	{"BEV-HOT-001", 1, "CUP", []ingredientDef{{"RAW-CHO-001", 0.04, "LITRE"}, {"RAW-MLK-001", 0.25, "LITRE"}, {"RAW-CRM-001", 0.03, "LITRE"}}},

	// ── Cold Beverages ────────────────────────────────────────────────────
	{"BEV-ICE-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.2, "LITRE"}, {"RAW-ICE-001", 0.15, "KG"}}},
	{"BEV-ICE-002", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-ICE-001", 0.15, "KG"}}},
	{"BEV-FRP-001", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.2, "LITRE"}, {"RAW-CAR-001", 0.03, "LITRE"}, {"RAW-ICE-001", 0.15, "KG"}}},
	{"BEV-FRP-002", 1, "CUP", []ingredientDef{{"RAW-ESP-001", 0.018, "KG"}, {"RAW-MLK-001", 0.2, "LITRE"}, {"RAW-VAN-001", 0.03, "LITRE"}, {"RAW-ICE-001", 0.15, "KG"}}},
	{"BEV-SMO-001", 1, "CUP", []ingredientDef{{"RAW-MNG-001", 0.2, "KG"}, {"RAW-YGT-001", 0.15, "LITRE"}, {"RAW-ICE-001", 0.1, "KG"}}},
	{"BEV-SMO-002", 1, "CUP", []ingredientDef{{"RAW-BRY-001", 0.15, "KG"}, {"RAW-BNA-001", 0.1, "KG"}, {"RAW-YGT-001", 0.15, "LITRE"}, {"RAW-ICE-001", 0.1, "KG"}}},
	{"BEV-JCE-001", 1, "CUP", []ingredientDef{{"RAW-ORG-001", 0.4, "KG"}}},

	// ── Sandwiches & Wraps ────────────────────────────────────────────────
	{"SND-CLB-001", 1, "PIECE", []ingredientDef{{"RAW-BRD-001", 3, "SLICE"}, {"RAW-CKN-001", 0.15, "KG"}, {"RAW-BCN-001", 0.05, "KG"}, {"RAW-LET-001", 0.03, "KG"}, {"RAW-TMT-001", 0.05, "KG"}}},
	{"SND-GRL-001", 1, "PIECE", []ingredientDef{{"RAW-BRD-001", 1, "PIECE"}, {"RAW-CKN-001", 0.15, "KG"}, {"RAW-PST-001", 0.02, "LITRE"}, {"RAW-CHZ-001", 0.05, "KG"}}},
	{"SND-VEG-001", 1, "PIECE", []ingredientDef{{"RAW-TRT-001", 1, "PIECE"}, {"RAW-HUM-001", 0.05, "KG"}, {"RAW-AVO-001", 0.5, "PIECE"}, {"RAW-VEG-001", 0.1, "KG"}}},
	{"SND-BLT-001", 1, "PIECE", []ingredientDef{{"RAW-BRD-001", 2, "SLICE"}, {"RAW-BCN-001", 0.06, "KG"}, {"RAW-LET-001", 0.03, "KG"}, {"RAW-TMT-001", 0.05, "KG"}}},
	{"SND-TUN-001", 1, "PIECE", []ingredientDef{{"RAW-RYE-001", 2, "SLICE"}, {"RAW-TNA-001", 0.1, "KG"}, {"RAW-CDD-001", 0.04, "KG"}}},

	// ── Salads ────────────────────────────────────────────────────────────
	{"SAL-CES-001", 1, "BOWL", []ingredientDef{{"RAW-LET-001", 0.15, "KG"}, {"RAW-CRT-001", 0.03, "KG"}, {"RAW-PRM-001", 0.03, "KG"}, {"RAW-CSR-001", 0.04, "LITRE"}}},
	{"SAL-GRK-001", 1, "BOWL", []ingredientDef{{"RAW-CUC-001", 0.1, "KG"}, {"RAW-TMT-001", 0.1, "KG"}, {"RAW-OLV-001", 0.03, "KG"}, {"RAW-FTA-001", 0.05, "KG"}, {"RAW-OVO-001", 0.02, "LITRE"}}},

	// ── Light Bites ───────────────────────────────────────────────────────
	{"BTE-SAM-001", 1, "SERVING", []ingredientDef{{"RAW-SAM-001", 3, "PIECE"}, {"RAW-VEG-001", 0.1, "KG"}, {"RAW-TMR-001", 0.03, "LITRE"}}},
	{"BTE-SPR-001", 1, "SERVING", []ingredientDef{{"RAW-SPW-001", 4, "PIECE"}, {"RAW-VEG-001", 0.1, "KG"}, {"RAW-SCS-001", 0.03, "LITRE"}}},

	// ── Main Courses ──────────────────────────────────────────────────────
	{"MIN-GRL-001", 1, "PLATE", []ingredientDef{{"RAW-BEF-001", 0.25, "KG"}, {"RAW-POT-001", 0.2, "KG"}, {"RAW-VEG-001", 0.15, "KG"}, {"RAW-BTR-001", 0.02, "KG"}}},
	{"MIN-GRL-002", 1, "PLATE", []ingredientDef{{"RAW-CKN-001", 0.25, "KG"}, {"RAW-RIC-001", 0.15, "KG"}, {"RAW-VEG-001", 0.15, "KG"}}},
	{"MIN-CUR-001", 1, "PLATE", []ingredientDef{{"RAW-CKN-001", 0.25, "KG"}, {"RAW-CRY-001", 0.02, "KG"}, {"RAW-RIC-001", 0.15, "KG"}, {"RAW-NAN-001", 1, "PIECE"}}},
	{"MIN-CUR-002", 1, "PLATE", []ingredientDef{{"RAW-BEF-001", 0.25, "KG"}, {"RAW-POT-001", 0.15, "KG"}, {"RAW-VEG-001", 0.15, "KG"}, {"RAW-UGL-001", 0.15, "KG"}}},
	{"MIN-SEA-001", 1, "PLATE", []ingredientDef{{"RAW-FSH-001", 0.2, "KG"}, {"RAW-FLR-001", 0.05, "KG"}, {"RAW-POT-001", 0.2, "KG"}, {"RAW-TAR-001", 0.03, "LITRE"}}},
	{"MIN-PAS-001", 1, "PLATE", []ingredientDef{{"RAW-SPG-001", 0.15, "KG"}, {"RAW-MNC-001", 0.15, "KG"}, {"RAW-TMC-001", 0.1, "LITRE"}, {"RAW-PRM-001", 0.02, "KG"}, {"RAW-BRD-001", 1, "SLICE"}}},
	{"MIN-RIC-001", 1, "BOWL", []ingredientDef{{"RAW-RIC-001", 0.2, "KG"}, {"RAW-PIL-001", 0.02, "KG"}, {"RAW-CKN-001", 0.15, "KG"}}},

	// ── Breakfast ─────────────────────────────────────────────────────────
	{"BRK-FUL-001", 1, "PLATE", []ingredientDef{{"RAW-EGG-001", 2, "PIECE"}, {"RAW-BCN-001", 0.06, "KG"}, {"RAW-SSG-001", 0.1, "KG"}, {"RAW-BNS-001", 0.1, "KG"}, {"RAW-BRD-001", 2, "SLICE"}, {"RAW-TMT-001", 0.05, "KG"}}},
	{"BRK-PAN-001", 1, "PLATE", []ingredientDef{{"RAW-PNC-001", 0.15, "KG"}, {"RAW-MPS-001", 0.03, "LITRE"}, {"RAW-BRY-001", 0.05, "KG"}, {"RAW-EGG-001", 1, "PIECE"}}},
	{"BRK-AVT-001", 1, "PLATE", []ingredientDef{{"RAW-BRD-001", 2, "SLICE"}, {"RAW-AVO-001", 1, "PIECE"}, {"RAW-EGG-001", 1, "PIECE"}}},
	{"BRK-OAT-001", 1, "BOWL", []ingredientDef{{"RAW-OAT-001", 0.08, "KG"}, {"RAW-ALM-001", 0.2, "LITRE"}, {"RAW-HNY-001", 0.02, "KG"}, {"RAW-BRY-001", 0.03, "KG"}}},

	// ── Pizza ─────────────────────────────────────────────────────────────
	{"PIZ-MAR-001", 1, "PIECE", []ingredientDef{{"RAW-PZD-001", 1, "PIECE"}, {"RAW-TMC-001", 0.1, "LITRE"}, {"RAW-CHZ-001", 0.15, "KG"}, {"RAW-BSL-001", 0.01, "KG"}}},
	{"PIZ-PEP-001", 1, "PIECE", []ingredientDef{{"RAW-PZD-001", 1, "PIECE"}, {"RAW-TMC-001", 0.1, "LITRE"}, {"RAW-CHZ-001", 0.12, "KG"}, {"RAW-PEP-001", 0.08, "KG"}}},
}

func seedRecipes(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, rd := range recipeDefs {
		recipeID := recipeUUID(tenantID, rd.SKU)

		// Find the matching item name from catalogItemDefs.
		var recipeName string
		for _, item := range catalogItemDefs {
			if item.SKU == rd.SKU {
				recipeName = item.Name
				break
			}
		}
		if recipeName == "" {
			recipeName = rd.SKU
		}

		// Upsert recipe.
		_, err := client.Recipe.Get(ctx, recipeID)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.Recipe.Create().
				SetID(recipeID).
				SetTenantID(tenantID).
				SetSku(rd.SKU).
				SetName(recipeName).
				SetOutputQty(rd.OutputQty).
				SetUnitOfMeasure(rd.UOM).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return fmt.Errorf("create recipe %s: %w", rd.SKU, createErr)
			}
			log.Printf("recipe created: %s — %s", rd.SKU, recipeName)
		case err != nil:
			return fmt.Errorf("check recipe %s: %w", rd.SKU, err)
		default:
			if _, updateErr := client.Recipe.UpdateOneID(recipeID).
				SetName(recipeName).
				SetOutputQty(rd.OutputQty).
				SetUnitOfMeasure(rd.UOM).
				SetIsActive(true).
				Save(ctx); updateErr != nil {
				return fmt.Errorf("update recipe %s: %w", rd.SKU, updateErr)
			}
		}

		// Seed ingredients for this recipe.
		for i, ing := range rd.Ingredients {
			rawItemID := itemUUID(tenantID, ing.RawSKU)
			ingID := recipeIngredientUUID(recipeID, ing.RawSKU)

			// Check if already exists by deterministic ID.
			_, getErr := client.RecipeIngredient.Get(ctx, ingID)
			switch {
			case ent.IsNotFound(getErr):
				if _, createErr := client.RecipeIngredient.Create().
					SetID(ingID).
					SetRecipeID(recipeID).
					SetItemID(rawItemID).
					SetItemSku(ing.RawSKU).
					SetQuantity(ing.Qty).
					SetUnitOfMeasure(ing.UOM).
					SetDisplayOrder(i).
					Save(ctx); createErr != nil {
					return fmt.Errorf("create ingredient %s→%s: %w", rd.SKU, ing.RawSKU, createErr)
				}
			case getErr != nil:
				return fmt.Errorf("check ingredient %s→%s: %w", rd.SKU, ing.RawSKU, getErr)
			default:
				if _, updateErr := client.RecipeIngredient.UpdateOneID(ingID).
					SetQuantity(ing.Qty).
					SetUnitOfMeasure(ing.UOM).
					SetDisplayOrder(i).
					Save(ctx); updateErr != nil {
					return fmt.Errorf("update ingredient %s→%s: %w", rd.SKU, ing.RawSKU, updateErr)
				}
			}

		}
	}

	log.Printf("seeded %d recipes with BOM ingredients", len(recipeDefs))
	return nil
}
