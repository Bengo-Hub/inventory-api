package stock

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
)

func TestLastPurchasePrice(t *testing.T) {
	cases := []struct {
		name string
		itm  *ent.Item
		cost float64
		want *float64
	}{
		{"no purchase price on file keeps reading cost_price", &ent.Item{}, 100, nil},
		{"pack price follows the newest unit cost", &ent.Item{PurchasePrice: fptr(1200), PurchasePackSize: fptr(12)}, 110, fptr(1320)},
		{"no pack size means price per stock unit", &ent.Item{PurchasePrice: fptr(50)}, 55, fptr(55)},
		{"zero pack size treated as one unit", &ent.Item{PurchasePrice: fptr(50), PurchasePackSize: fptr(0)}, 60, fptr(60)},
		{"zero cost leaves the price alone", &ent.Item{PurchasePrice: fptr(50)}, 0, nil},
	}
	for _, c := range cases {
		got := lastPurchasePrice(c.itm, c.cost)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
