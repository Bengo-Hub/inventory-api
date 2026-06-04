package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// detergentItems returns the raw chemicals (INGREDIENT) a small detergent maker
// buys plus the finished detergents (RECIPE) it manufactures. Demonstrates the
// manufacturing pipeline: chemicals → ProductionBatch → finished detergent stock.
func detergentItems() []itemDef {
	return []itemDef{
		// ── Raw chemicals (purchased inputs) ──
		{"RAW-SLES-001", "Sodium Lauryl Ether Sulfate (SLES)", "Primary anionic surfactant", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 1000, []string{"chemical", "surfactant"}, ptr(320.0)},
		{"RAW-CAUS-001", "Caustic Soda (NaOH)", "Sodium hydroxide flakes", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 500, []string{"chemical", "alkali"}, ptr(180.0)},
		{"RAW-SODA-001", "Soda Ash (Sodium Carbonate)", "Builder / water softener", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 600, []string{"chemical", "builder"}, ptr(90.0)},
		{"RAW-STPP-001", "STPP (Sodium Tripolyphosphate)", "Builder / anti-redeposition", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 300, []string{"chemical", "builder"}, ptr(260.0)},
		{"RAW-SILI-001", "Sodium Silicate", "Corrosion inhibitor / builder", "chemicals", entitem.TypeINGREDIENT, "LITRE", mediaPlaceholder, 400, []string{"chemical"}, ptr(140.0)},
		{"RAW-CDEA-001", "CDEA (Coconut Diethanolamide)", "Foam booster / thickener", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 150, []string{"chemical", "surfactant"}, ptr(600.0)},
		{"RAW-SALT-001", "Salt (NaCl)", "Viscosity modifier", "chemicals", entitem.TypeINGREDIENT, "KG", mediaPlaceholder, 800, []string{"chemical"}, ptr(25.0)},
		{"RAW-FRAG-001", "Fragrance Oil (Lemon)", "Perfume", "chemicals", entitem.TypeINGREDIENT, "LITRE", mediaPlaceholder, 100, []string{"chemical", "fragrance"}, ptr(1500.0)},
		{"RAW-DYE-001", "Colourant / Dye", "Liquid colourant", "chemicals", entitem.TypeINGREDIENT, "LITRE", mediaPlaceholder, 50, []string{"chemical", "colour"}, ptr(1200.0)},
		{"RAW-PRES-001", "Preservative (Kathon)", "Anti-microbial preservative", "chemicals", entitem.TypeINGREDIENT, "LITRE", mediaPlaceholder, 40, []string{"chemical"}, ptr(1800.0)},
		{"RAW-H2O-001", "Purified Water", "Treated process water", "chemicals", entitem.TypeINGREDIENT, "LITRE", mediaPlaceholder, 5000, []string{"chemical"}, ptr(2.0)},

		// ── Finished detergents (manufactured via recipes/BOMs) ──
		{"FIN-DISH-001", "Dishwashing Liquid 750ml", "Lemon dishwashing liquid", "detergents", entitem.TypeRECIPE, "PIECE", mediaPlaceholder, 148, []string{"detergent", "dishwashing"}, nil},
		{"FIN-HAND-001", "Liquid Hand Soap 500ml", "Moisturising liquid hand soap", "detergents", entitem.TypeRECIPE, "PIECE", mediaPlaceholder, 0, []string{"detergent", "handsoap"}, nil},
		{"FIN-MULT-001", "Multipurpose Cleaner 1L", "All-surface multipurpose cleaner", "detergents", entitem.TypeRECIPE, "PIECE", mediaPlaceholder, 0, []string{"detergent", "cleaner"}, nil},
		{"FIN-BLCH-001", "Bleach 1L", "Sodium hypochlorite bleach", "detergents", entitem.TypeRECIPE, "PIECE", mediaPlaceholder, 0, []string{"detergent", "bleach"}, nil},
		{"FIN-BAR-001", "Laundry Bar Soap 200g", "Hand-wash laundry bar", "detergents", entitem.TypeRECIPE, "PIECE", mediaPlaceholder, 0, []string{"detergent", "barsoap"}, nil},
	}
}

// detergentRecipeDefs are the BOMs/formulas mapping chemicals → finished detergents.
// Appended to the shared recipeDefs in init() so they seed for codevertex-demo and
// get costs auto-calculated by recalculateAllRecipeCosts.
var detergentRecipeDefs = []recipeDef{
	{"FIN-DISH-001", 150, "PIECE", ptr(45.0), []ingredientDef{
		{"RAW-SLES-001", 8, "KG", 1.0},
		{"RAW-CDEA-001", 1.5, "KG", 1.0},
		{"RAW-SALT-001", 2, "KG", 0.0},
		{"RAW-STPP-001", 1, "KG", 1.0},
		{"RAW-FRAG-001", 1.5, "LITRE", 2.0},
		{"RAW-DYE-001", 0.2, "LITRE", 2.0},
		{"RAW-PRES-001", 0.3, "LITRE", 1.0},
		{"RAW-H2O-001", 85, "LITRE", 0.0},
	}},
	{"FIN-HAND-001", 200, "PIECE", ptr(50.0), []ingredientDef{
		{"RAW-SLES-001", 6, "KG", 1.0},
		{"RAW-CDEA-001", 1, "KG", 1.0},
		{"RAW-SALT-001", 1.5, "KG", 0.0},
		{"RAW-FRAG-001", 1.2, "LITRE", 2.0},
		{"RAW-DYE-001", 0.15, "LITRE", 2.0},
		{"RAW-PRES-001", 0.25, "LITRE", 1.0},
		{"RAW-H2O-001", 90, "LITRE", 0.0},
	}},
	{"FIN-MULT-001", 120, "PIECE", ptr(40.0), []ingredientDef{
		{"RAW-SLES-001", 3, "KG", 1.0},
		{"RAW-SODA-001", 1, "KG", 0.0},
		{"RAW-SILI-001", 1, "LITRE", 1.0},
		{"RAW-FRAG-001", 1, "LITRE", 2.0},
		{"RAW-DYE-001", 0.1, "LITRE", 2.0},
		{"RAW-H2O-001", 95, "LITRE", 0.0},
	}},
	{"FIN-BLCH-001", 150, "PIECE", ptr(35.0), []ingredientDef{
		{"RAW-CAUS-001", 2, "KG", 1.0},
		{"RAW-SILI-001", 0.5, "LITRE", 1.0},
		{"RAW-H2O-001", 95, "LITRE", 0.0},
	}},
	{"FIN-BAR-001", 500, "PIECE", ptr(45.0), []ingredientDef{
		{"RAW-CAUS-001", 12, "KG", 1.0},
		{"RAW-SODA-001", 8, "KG", 0.0},
		{"RAW-FRAG-001", 1.5, "LITRE", 2.0},
		{"RAW-H2O-001", 25, "LITRE", 0.0},
	}},
}

func init() {
	recipeDefs = append(recipeDefs, detergentRecipeDefs...)
}
