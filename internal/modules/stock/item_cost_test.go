package stock

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
)

func fptr(v float64) *float64 { return &v }

// TestItemCostPrice_PerBaseUnit: a cost entered per purchase unit on a gram-stocked item is
// replaced by the per-base-unit cost; a sane per-base-unit cost is kept.
func TestItemCostPrice_PerBaseUnit(t *testing.T) {
	cases := []struct {
		name string
		itm  *ent.Item
		unit string
		want float64
	}{
		{"nil cost", &ent.Item{}, "g", 0},
		{"no purchase data", &ent.Item{CostPrice: fptr(350)}, "g", 350},
		{"passion fruit per kg on grams", &ent.Item{CostPrice: fptr(350), PurchasePrice: fptr(200), PurchasePackSize: fptr(1000), YieldPct: fptr(0.7)}, "g", 200.0 / 1000 / 0.7},
		{"drum sticks per kg on grams, pack 1", &ent.Item{CostPrice: fptr(650), PurchasePrice: fptr(0.65), PurchasePackSize: fptr(1)}, "g", 0.65},
		{"mozzarella weighted per gram kept", &ent.Item{CostPrice: fptr(1.0868), PurchasePrice: fptr(1100), PurchasePackSize: fptr(1000), YieldPct: fptr(1)}, "g", 1.0868},
		{"bottle item never rescaled", &ent.Item{CostPrice: fptr(450), PurchasePrice: fptr(9), PurchasePackSize: fptr(50)}, "btl", 450},
		{"unknown unit never rescaled", &ent.Item{CostPrice: fptr(350), PurchasePrice: fptr(200), PurchasePackSize: fptr(1000)}, "", 350},
	}
	for _, c := range cases {
		if got := itemCostPerStockUnit(c.itm, c.unit); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
