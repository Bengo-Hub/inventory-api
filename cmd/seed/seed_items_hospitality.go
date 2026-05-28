package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// hospitalityItems returns catalog items for the hospitality use case:
// food/beverage menu items (RECIPE/GOODS) and raw ingredients (INGREDIENT).
func hospitalityItems() []itemDef {
	return []itemDef{
		// ── Hot Beverages (RECIPE) ─────────────────────────────────────────────
		{"BEV-ESP-001", "Espresso", "Single shot of rich espresso", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 500, nil, nil},
		{"BEV-ESP-002", "Double Espresso", "Double shot espresso", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 500, nil, nil},
		{"BEV-LAT-001", "Caffe Latte", "Espresso with steamed milk", "hot-beverages", entitem.TypeRECIPE, "CUP", imgCappuccino, 400, nil, nil},
		{"BEV-CAP-001", "Cappuccino", "Espresso with frothed milk and cocoa", "hot-beverages", entitem.TypeRECIPE, "CUP", imgCappuccino, 400, nil, nil},
		{"BEV-AME-001", "Americano", "Espresso with hot water", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 500, nil, nil},
		{"BEV-MOC-001", "Mocha", "Espresso, chocolate, steamed milk, whipped cream", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 300, nil, nil},
		{"BEV-MAC-001", "Macchiato", "Espresso with a dash of milk foam", "hot-beverages", entitem.TypeRECIPE, "CUP", imgEspresso, 400, nil, nil},
		{"BEV-TEA-001", "Kenya AA Black Tea", "Premium Kenyan black tea", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 600, nil, nil},
		{"BEV-TEA-002", "Masala Chai", "Spiced tea latte with cardamom and ginger", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 400, nil, nil},
		{"BEV-HOT-001", "Hot Chocolate", "Rich hot chocolate with whipped cream", "hot-beverages", entitem.TypeRECIPE, "CUP", imgHotCoffee, 300, nil, nil},

		// ── Cold Beverages (RECIPE) ────────────────────────────────────────────
		{"BEV-ICE-001", "Iced Latte", "Chilled espresso with cold milk over ice", "cold-beverages", entitem.TypeRECIPE, "CUP", imgIcedLatte, 300, nil, nil},
		{"BEV-ICE-002", "Iced Americano", "Espresso over ice with cold water", "cold-beverages", entitem.TypeRECIPE, "CUP", imgIcedLatte, 300, nil, nil},
		{"BEV-FRP-001", "Caramel Frappe", "Blended iced coffee with caramel drizzle", "cold-beverages", entitem.TypeRECIPE, "CUP", imgMilkshake, 200, nil, nil},
		{"BEV-FRP-002", "Vanilla Frappe", "Blended iced coffee with vanilla", "cold-beverages", entitem.TypeRECIPE, "CUP", imgMilkshake, 200, nil, nil},
		{"BEV-SMO-001", "Mango Smoothie", "Fresh mango blended with yoghurt", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 150, nil, nil},
		{"BEV-SMO-002", "Mixed Berry Smoothie", "Strawberry, blueberry, and banana blend", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 150, nil, nil},
		{"BEV-JCE-001", "Fresh Orange Juice", "Freshly squeezed orange juice", "cold-beverages", entitem.TypeRECIPE, "CUP", imgCocktail, 200, nil, nil},

		// ── Pastries & Bakery (GOODS) ──────────────────────────────────────────
		{"PST-CRO-001", "Butter Croissant", "Flaky French butter croissant", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 100, nil, nil},
		{"PST-CRO-002", "Chocolate Croissant", "Croissant filled with dark chocolate", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80, nil, nil},
		{"PST-MUF-001", "Blueberry Muffin", "Moist muffin loaded with blueberries", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80, nil, nil},
		{"PST-MUF-002", "Banana Walnut Muffin", "Banana muffin with crunchy walnuts", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 80, nil, nil},
		{"PST-CKE-001", "Carrot Cake Slice", "Spiced carrot cake with cream cheese frosting", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 50, nil, nil},
		{"PST-CKE-002", "Red Velvet Cake Slice", "Classic red velvet with vanilla cream cheese", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 40, nil, nil},
		{"PST-CKE-003", "Chocolate Fudge Cake Slice", "Rich chocolate fudge layer cake", "pastries", entitem.TypeGOODS, "SLICE", imgLavaCake, 40, nil, nil},
		{"PST-DAN-001", "Danish Pastry", "Flaky pastry with custard and fruit", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 60, nil, nil},
		{"PST-SCO-001", "Classic Scone", "Buttermilk scone with clotted cream and jam", "pastries", entitem.TypeGOODS, "PIECE", imgDessert, 70, nil, nil},

		// ── Sandwiches & Wraps (RECIPE) ────────────────────────────────────────
		{"SND-CLB-001", "Club Sandwich", "Triple-decker with chicken, bacon, lettuce, tomato", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 60, nil, nil},
		{"SND-GRL-001", "Grilled Chicken Panini", "Grilled chicken, pesto, mozzarella on ciabatta", "sandwiches", entitem.TypeRECIPE, "PIECE", imgChicken, 50, nil, nil},
		{"SND-VEG-001", "Veggie Wrap", "Hummus, avocado, roasted vegetables in tortilla", "sandwiches", entitem.TypeRECIPE, "PIECE", imgSalad, 50, nil, nil},
		{"SND-BLT-001", "BLT Sandwich", "Bacon, lettuce, tomato on toasted sourdough", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 50, nil, nil},
		{"SND-TUN-001", "Tuna Melt", "Tuna salad with melted cheddar on rye bread", "sandwiches", entitem.TypeRECIPE, "PIECE", imgMain1, 40, nil, nil},

		// ── Salads (RECIPE) ────────────────────────────────────────────────────
		{"SAL-CES-001", "Caesar Salad", "Romaine, croutons, parmesan, caesar dressing", "salads", entitem.TypeRECIPE, "BOWL", imgSalad, 40, nil, nil},
		{"SAL-GRK-001", "Greek Salad", "Cucumber, tomato, olives, feta, olive oil", "salads", entitem.TypeRECIPE, "BOWL", imgSalad, 40, nil, nil},

		// ── Light Bites (RECIPE) ───────────────────────────────────────────────
		{"BTE-SAM-001", "Samosa (3pc)", "Crispy vegetable samosas with tamarind chutney", "light-bites", entitem.TypeRECIPE, "SERVING", imgMain2, 80, nil, nil},
		{"BTE-SPR-001", "Spring Rolls (4pc)", "Crispy vegetable spring rolls with sweet chilli sauce", "light-bites", entitem.TypeRECIPE, "SERVING", imgMain2, 60, nil, nil},

		// ── Main Courses (RECIPE) ──────────────────────────────────────────────
		{"MIN-GRL-001", "Grilled Beef Fillet", "250g beef fillet with pepper sauce, mash and seasonal veg", "main-courses", entitem.TypeRECIPE, "PLATE", imgMain1, 30, nil, nil},
		{"MIN-GRL-002", "Grilled Chicken Breast", "Herb-marinated chicken with gravy, rice and vegetables", "main-courses", entitem.TypeRECIPE, "PLATE", imgChickenUgali, 40, nil, nil},
		{"MIN-CUR-001", "Chicken Curry", "Spiced chicken curry with basmati rice and naan", "main-courses", entitem.TypeRECIPE, "PLATE", imgChicken, 40, nil, nil},
		{"MIN-CUR-002", "Beef Stew", "Tender beef stew with potatoes and carrots, served with ugali or rice", "main-courses", entitem.TypeRECIPE, "PLATE", imgPilau, 40, nil, nil},
		{"MIN-SEA-001", "Fish and Chips", "Beer-battered fish with chips and tartar sauce", "main-courses", entitem.TypeRECIPE, "PLATE", imgFish, 30, nil, nil},
		{"MIN-PAS-001", "Spaghetti Bolognese", "Classic beef bolognese with parmesan and garlic bread", "main-courses", entitem.TypeRECIPE, "PLATE", imgMain1, 30, nil, nil},
		{"MIN-RIC-001", "Pilau Rice Bowl", "Spiced pilau rice with choice of beef, chicken or veg", "main-courses", entitem.TypeRECIPE, "BOWL", imgPilau, 50, nil, nil},

		// ── Breakfast (RECIPE) ─────────────────────────────────────────────────
		{"BRK-FUL-001", "Full English Breakfast", "Eggs, bacon, sausage, beans, toast, tomato", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50, nil, nil},
		{"BRK-PAN-001", "Pancake Stack", "Fluffy pancakes with maple syrup and berries", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50, nil, nil},
		{"BRK-AVT-001", "Avocado Toast", "Smashed avocado on sourdough with poached egg", "breakfast", entitem.TypeRECIPE, "PLATE", imgBreakfast, 50, nil, nil},
		{"BRK-OAT-001", "Overnight Oats", "Oats soaked in almond milk with fresh fruits and honey", "breakfast", entitem.TypeRECIPE, "BOWL", imgOats, 40, nil, nil},

		// ── Pizza (RECIPE) ─────────────────────────────────────────────────────
		{"PIZ-MAR-001", "Margherita Pizza", "Fresh mozzarella, tomato sauce, and basil", "pizza", entitem.TypeRECIPE, "PIECE", imgPizza, 30, nil, nil},
		{"PIZ-PEP-001", "Pepperoni Pizza", "Classic pepperoni with mozzarella and tomato sauce", "pizza", entitem.TypeRECIPE, "PIECE", imgPizza, 30, nil, nil},

		// ── Raw Ingredients (INGREDIENT with cost_price) ───────────────────────
		{"RAW-ESP-001", "Espresso Beans", "Premium roasted espresso beans", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 50, nil, ptr(2200.0)},
		{"RAW-MLK-001", "Fresh Milk", "Full-cream fresh milk", "hot-beverages", entitem.TypeINGREDIENT, "LITRE", "", 100, nil, ptr(85.0)},
		{"RAW-CHO-001", "Chocolate Syrup", "Dark chocolate syrup", "hot-beverages", entitem.TypeINGREDIENT, "LITRE", "", 20, nil, ptr(320.0)},
		{"RAW-SGR-001", "Sugar", "White granulated sugar", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 50, nil, ptr(120.0)},
		{"RAW-VAN-001", "Vanilla Syrup", "Vanilla flavoured syrup", "cold-beverages", entitem.TypeINGREDIENT, "LITRE", "", 15, nil, ptr(380.0)},
		{"RAW-CAR-001", "Caramel Syrup", "Caramel flavoured syrup", "cold-beverages", entitem.TypeINGREDIENT, "LITRE", "", 15, nil, ptr(380.0)},
		{"RAW-ICE-001", "Ice Cubes", "Filtered water ice cubes", "cold-beverages", entitem.TypeINGREDIENT, "KG", "", 200, nil, ptr(20.0)},
		{"RAW-CRM-001", "Whipped Cream", "Ready whipped cream", "hot-beverages", entitem.TypeINGREDIENT, "LITRE", "", 20, nil, ptr(450.0)},
		{"RAW-COC-001", "Cocoa Powder", "Dutch-process cocoa powder", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(950.0)},
		{"RAW-TEA-001", "Kenya AA Tea Leaves", "Premium Kenyan black tea leaves", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(1100.0)},
		{"RAW-GNG-001", "Ginger", "Fresh ginger root", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(200.0)},
		{"RAW-CDM-001", "Cardamom", "Ground cardamom spice", "hot-beverages", entitem.TypeINGREDIENT, "KG", "", 5, nil, ptr(1800.0)},
		{"RAW-MNG-001", "Fresh Mango", "Ripe mangoes", "cold-beverages", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(180.0)},
		{"RAW-YGT-001", "Yoghurt", "Plain natural yoghurt", "cold-beverages", entitem.TypeINGREDIENT, "LITRE", "", 20, nil, ptr(160.0)},
		{"RAW-BRY-001", "Mixed Berries", "Frozen mixed berries (strawberry, blueberry)", "cold-beverages", entitem.TypeINGREDIENT, "KG", "", 15, nil, ptr(650.0)},
		{"RAW-BNA-001", "Bananas", "Fresh bananas", "cold-beverages", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(80.0)},
		{"RAW-ORG-001", "Fresh Oranges", "Navel oranges for juicing", "cold-beverages", entitem.TypeINGREDIENT, "KG", "", 40, nil, ptr(120.0)},
		{"RAW-BRD-001", "Sourdough Bread", "Artisan sourdough loaf", "sandwiches", entitem.TypeINGREDIENT, "PIECE", "", 30, nil, ptr(280.0)},
		{"RAW-CKN-001", "Chicken Breast", "Fresh boneless chicken breast", "main-courses", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(850.0)},
		{"RAW-BCN-001", "Bacon", "Smoked streaky bacon", "sandwiches", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(1200.0)},
		{"RAW-LET-001", "Lettuce", "Fresh iceberg and romaine lettuce", "salads", entitem.TypeINGREDIENT, "KG", "", 25, nil, ptr(150.0)},
		{"RAW-TMT-001", "Tomatoes", "Fresh vine tomatoes", "salads", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(100.0)},
		{"RAW-CHZ-001", "Mozzarella Cheese", "Fresh mozzarella", "pizza", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(1400.0)},
		{"RAW-PRM-001", "Parmesan Cheese", "Aged parmesan block", "salads", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(1800.0)},
		{"RAW-PST-001", "Pesto Sauce", "Basil pesto", "sandwiches", entitem.TypeINGREDIENT, "LITRE", "", 10, nil, ptr(850.0)},
		{"RAW-TRT-001", "Tortilla Wraps", "Large flour tortilla wraps", "sandwiches", entitem.TypeINGREDIENT, "PIECE", "", 50, nil, ptr(35.0)},
		{"RAW-HUM-001", "Hummus", "Classic chickpea hummus", "sandwiches", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(650.0)},
		{"RAW-AVO-001", "Avocado", "Fresh ripe avocados", "salads", entitem.TypeINGREDIENT, "PIECE", "", 40, nil, ptr(80.0)},
		{"RAW-TNA-001", "Canned Tuna", "Tuna chunks in brine", "sandwiches", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(750.0)},
		{"RAW-CDD-001", "Cheddar Cheese", "Mature cheddar slices", "sandwiches", entitem.TypeINGREDIENT, "KG", "", 15, nil, ptr(1100.0)},
		{"RAW-RYE-001", "Rye Bread", "Dark rye bread loaf", "sandwiches", entitem.TypeINGREDIENT, "PIECE", "", 20, nil, ptr(220.0)},
		{"RAW-OLV-001", "Olives", "Kalamata olives", "salads", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(900.0)},
		{"RAW-FTA-001", "Feta Cheese", "Greek feta cheese", "salads", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(1200.0)},
		{"RAW-CUC-001", "Cucumber", "Fresh cucumbers", "salads", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(80.0)},
		{"RAW-OVO-001", "Olive Oil", "Extra virgin olive oil", "salads", entitem.TypeINGREDIENT, "LITRE", "", 15, nil, ptr(1100.0)},
		{"RAW-CRT-001", "Croutons", "Seasoned croutons", "salads", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(350.0)},
		{"RAW-CSR-001", "Caesar Dressing", "Classic caesar dressing", "salads", entitem.TypeINGREDIENT, "LITRE", "", 10, nil, ptr(450.0)},
		{"RAW-SAM-001", "Samosa Wraps", "Pre-made samosa pastry wraps", "light-bites", entitem.TypeINGREDIENT, "PIECE", "", 100, nil, ptr(15.0)},
		{"RAW-SPW-001", "Spring Roll Wraps", "Rice paper spring roll wraps", "light-bites", entitem.TypeINGREDIENT, "PIECE", "", 100, nil, ptr(12.0)},
		{"RAW-BEF-001", "Beef Fillet", "Prime beef fillet steaks", "main-courses", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(1800.0)},
		{"RAW-RIC-001", "Basmati Rice", "Long-grain basmati rice", "main-courses", entitem.TypeINGREDIENT, "KG", "", 50, nil, ptr(180.0)},
		{"RAW-NAN-001", "Naan Bread", "Fresh tandoori naan", "main-courses", entitem.TypeINGREDIENT, "PIECE", "", 40, nil, ptr(45.0)},
		{"RAW-POT-001", "Potatoes", "Fresh white potatoes", "main-courses", entitem.TypeINGREDIENT, "KG", "", 50, nil, ptr(60.0)},
		{"RAW-FSH-001", "White Fish Fillet", "Beer-batter-ready cod/tilapia fillets", "main-courses", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(950.0)},
		{"RAW-SPG-001", "Spaghetti", "Dried spaghetti pasta", "main-courses", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(200.0)},
		{"RAW-MNC-001", "Minced Beef", "Lean minced beef", "main-courses", entitem.TypeINGREDIENT, "KG", "", 25, nil, ptr(900.0)},
		{"RAW-PIL-001", "Pilau Spice Mix", "Traditional pilau masala", "main-courses", entitem.TypeINGREDIENT, "KG", "", 5, nil, ptr(1200.0)},
		{"RAW-EGG-001", "Eggs", "Free-range eggs", "breakfast", entitem.TypeINGREDIENT, "PIECE", "", 200, nil, ptr(18.0)},
		{"RAW-SSG-001", "Pork Sausages", "Breakfast pork sausages", "breakfast", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(750.0)},
		{"RAW-BNS-001", "Baked Beans", "Tinned baked beans", "breakfast", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(180.0)},
		{"RAW-PNC-001", "Pancake Mix", "Ready-mix pancake batter", "breakfast", entitem.TypeINGREDIENT, "KG", "", 15, nil, ptr(250.0)},
		{"RAW-MPS-001", "Maple Syrup", "Canadian maple syrup", "breakfast", entitem.TypeINGREDIENT, "LITRE", "", 10, nil, ptr(950.0)},
		{"RAW-OAT-001", "Rolled Oats", "Organic rolled oats", "breakfast", entitem.TypeINGREDIENT, "KG", "", 20, nil, ptr(220.0)},
		{"RAW-ALM-001", "Almond Milk", "Unsweetened almond milk", "breakfast", entitem.TypeINGREDIENT, "LITRE", "", 15, nil, ptr(280.0)},
		{"RAW-HNY-001", "Honey", "Raw natural honey", "breakfast", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(850.0)},
		{"RAW-TMC-001", "Tomato Sauce", "Pizza and pasta tomato sauce", "pizza", entitem.TypeINGREDIENT, "LITRE", "", 20, nil, ptr(220.0)},
		{"RAW-BSL-001", "Fresh Basil", "Fresh basil leaves", "pizza", entitem.TypeINGREDIENT, "KG", "", 5, nil, ptr(800.0)},
		{"RAW-PEP-001", "Pepperoni", "Sliced pepperoni", "pizza", entitem.TypeINGREDIENT, "KG", "", 10, nil, ptr(1500.0)},
		{"RAW-PZD-001", "Pizza Dough", "Fresh pizza dough balls", "pizza", entitem.TypeINGREDIENT, "PIECE", "", 40, nil, ptr(80.0)},
		{"RAW-FLR-001", "Wheat Flour", "All-purpose wheat flour", "pastries", entitem.TypeINGREDIENT, "KG", "", 50, nil, ptr(90.0)},
		{"RAW-BTR-001", "Butter", "Unsalted butter blocks", "pastries", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(650.0)},
		{"RAW-CRY-001", "Curry Powder", "Blended curry powder", "main-courses", entitem.TypeINGREDIENT, "KG", "", 5, nil, ptr(800.0)},
		{"RAW-UGL-001", "Ugali Flour", "White maize flour for ugali", "main-courses", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(70.0)},
		{"RAW-TAR-001", "Tartar Sauce", "Ready tartar sauce", "main-courses", entitem.TypeINGREDIENT, "LITRE", "", 10, nil, ptr(380.0)},
		{"RAW-VEG-001", "Mixed Vegetables", "Seasonal mixed vegetables", "main-courses", entitem.TypeINGREDIENT, "KG", "", 30, nil, ptr(120.0)},
		{"RAW-TMR-001", "Tamarind Chutney", "Tangy tamarind chutney", "light-bites", entitem.TypeINGREDIENT, "LITRE", "", 5, nil, ptr(350.0)},
		{"RAW-SCS-001", "Sweet Chilli Sauce", "Thai sweet chilli dipping sauce", "light-bites", entitem.TypeINGREDIENT, "LITRE", "", 10, nil, ptr(280.0)},
	}
}
