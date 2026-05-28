package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// retailItems returns retail GOODS items for the retail use case.
func retailItems() []itemDef {
	return []itemDef{
		{"RTL-SHP-001", "Shampoo 400ml", "Moisturising shampoo for all hair types", "retail", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 40, []string{"haircare"}, ptr(380.0)},
		{"RTL-SHP-002", "Conditioner 400ml", "Deep conditioning treatment for frizz control", "retail", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 35, []string{"haircare"}, ptr(380.0)},
		{"RTL-BSP-001", "Body Wash 500ml", "Gentle daily body cleanser with aloe vera", "retail", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 50, []string{"bodycare"}, ptr(250.0)},
		{"RTL-LOT-001", "Body Lotion 250ml", "Intensive moisture lotion with shea butter", "retail", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 45, []string{"bodycare"}, ptr(320.0)},
		{"RTL-SPF-001", "Sunscreen SPF50 75ml", "Broad-spectrum UVA/UVB protection", "retail", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 30, []string{"skincare"}, ptr(600.0)},
		{"RTL-MSK-001", "Face Mask Sheet", "Hydrating sheet mask with hyaluronic acid", "retail", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 80, []string{"skincare"}, ptr(150.0)},
		{"RTL-DEO-001", "Roll-On Deodorant 50ml", "48hr protection anti-perspirant roll-on", "retail", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 60, []string{"personal-care"}, ptr(180.0)},
		{"RTL-NAP-001", "Nail Polish (assorted)", "Long-lasting gel-effect nail colour", "retail", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 100, []string{"nails", "beauty"}, ptr(250.0)},
		{"RTL-ACC-001", "Hair Accessories Set", "Set of clips, bands, and pins", "retail", entitem.TypeGOODS, "PACK", mediaPlaceholder, 35, []string{"accessories"}, ptr(200.0)},
		{"RTL-GFT-001", "Spa Gift Set", "Curated spa experience gift set with towel and products", "retail", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 20, []string{"gift", "spa"}, ptr(1200.0)},
	}
}
