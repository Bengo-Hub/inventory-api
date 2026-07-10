package stock

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
)

func itemWithUnit(abbrev string) *ent.Item {
	itm := &ent.Item{}
	if abbrev != "" {
		itm.Edges.Units = &ent.Unit{Abbreviation: abbrev}
	}
	return itm
}

// TestConvertToStockUnit pins the deduction-time unit handling: same-dimension recipe
// lines convert into the item's stock unit, count-stocked packaged goods bridge through
// unit_content (tots deplete fractional bottles), and cross-dimension lines with no
// bridge are refused (ok=false) so a raw number is never deducted.
func TestConvertToStockUnit(t *testing.T) {
	ml750 := 750.0

	tests := []struct {
		name   string
		itm    *ent.Item
		qty    float64
		from   string
		want   float64
		wantOK bool
	}{
		{"same unit passthrough", itemWithUnit("ml"), 25, "ml", 25, true},
		{"ml line, litre stock", itemWithUnit("l"), 500, "ml", 0.5, true},
		{"KG line, gram stock (case/synonym)", itemWithUnit("g"), 1.5, "KG", 1500, true},
		{"PIECE line, pc stock", itemWithUnit("pc"), 2, "PIECE", 2, true},
		{"no line unit — legacy passthrough", itemWithUnit("ml"), 30, "", 30, true},
		{"no stock unit — legacy passthrough", itemWithUnit(""), 30, "ml", 30, true},
		{
			"30ml tot from 750ml bottle stocked in pieces",
			func() *ent.Item {
				itm := itemWithUnit("pc")
				itm.UnitContentQty = &ml750
				itm.UnitContentUom = "ml"
				return itm
			}(),
			30, "ml", 0.04, true,
		},
		{
			"cross-dimension, no content bridge → refused",
			itemWithUnit("pc"), 25, "ml", 25, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ConvertToStockUnit(tt.itm, tt.qty, tt.from)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && round4(got) != tt.want {
				t.Fatalf("qty = %v, want %v", got, tt.want)
			}
		})
	}

	// 25 tots × 30ml deplete exactly one 750ml bottle (cumulative fractional pieces).
	bottle := itemWithUnit("pc")
	bottle.UnitContentQty = &ml750
	bottle.UnitContentUom = "ml"
	perTot, ok := ConvertToStockUnit(bottle, 30, "ml")
	if !ok || round4(perTot*25) != 1.0 {
		t.Fatalf("25 tots should deplete exactly 1 bottle, got %v (ok=%v)", perTot*25, ok)
	}
}

// TestIsNonDepleting pins the tracking-mode policy: explicit per-item mode always wins;
// items left on "default" follow the tenant recipe policy only when RECIPE-type.
func TestIsNonDepleting(t *testing.T) {
	cfgOn := &ent.TenantInventoryConfig{RecipeItemsNonDepletingDefault: true}
	cfgOff := &ent.TenantInventoryConfig{RecipeItemsNonDepletingDefault: false}

	mk := func(typ entitem.Type, mode entitem.StockTrackingMode) *ent.Item {
		return &ent.Item{Type: typ, StockTrackingMode: mode}
	}

	tests := []struct {
		name string
		itm  *ent.Item
		cfg  *ent.TenantInventoryConfig
		want bool
	}{
		{"explicit non_depleting wins", mk(entitem.TypeGOODS, entitem.StockTrackingModeNonDepleting), cfgOff, true},
		{"explicit tracked beats tenant default", mk(entitem.TypeRECIPE, entitem.StockTrackingModeTracked), cfgOn, false},
		{"default recipe follows tenant policy ON", mk(entitem.TypeRECIPE, entitem.StockTrackingModeDefault), cfgOn, true},
		{"default recipe follows tenant policy OFF", mk(entitem.TypeRECIPE, entitem.StockTrackingModeDefault), cfgOff, false},
		{"default GOODS ignores tenant policy", mk(entitem.TypeGOODS, entitem.StockTrackingModeDefault), cfgOn, false},
		{"default INGREDIENT ignores tenant policy", mk(entitem.TypeINGREDIENT, entitem.StockTrackingModeDefault), cfgOn, false},
		{"nil config → tracked", mk(entitem.TypeRECIPE, entitem.StockTrackingModeDefault), nil, false},
		{"nil item → tracked", nil, cfgOn, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonDepleting(tt.itm, tt.cfg); got != tt.want {
				t.Fatalf("isNonDepleting = %v, want %v", got, tt.want)
			}
		})
	}
}
