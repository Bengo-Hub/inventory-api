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
		want float64
	}{
		{"nil cost", &ent.Item{}, 0},
		{"no purchase data", &ent.Item{CostPrice: fptr(350)}, 350},
		{"passion fruit per kg on grams", &ent.Item{CostPrice: fptr(350), PurchasePrice: fptr(200), PurchasePackSize: fptr(1000), YieldPct: fptr(0.7)}, 200.0 / 1000 / 0.7},
		{"mozzarella weighted per gram kept", &ent.Item{CostPrice: fptr(1.0868), PurchasePrice: fptr(1100), PurchasePackSize: fptr(1000), YieldPct: fptr(1)}, 1.0868},
		{"bottle item kept", &ent.Item{CostPrice: fptr(2200), PurchasePrice: fptr(1600), PurchasePackSize: fptr(1)}, 2200},
	}
	for _, c := range cases {
		if got := itemCostPrice(c.itm); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
