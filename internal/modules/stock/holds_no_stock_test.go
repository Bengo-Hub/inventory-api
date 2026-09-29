package stock

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// TestHoldsNoStock pins which item types a sale, reservation or consumption skips: services and
// vouchers never hold stock (a sold print job showed -2 on hand before this guard), while goods,
// ingredients and equipment deplete, and RECIPE keeps its BOM path.
func TestHoldsNoStock(t *testing.T) {
	cases := map[item.Type]bool{
		item.TypeSERVICE:    true,
		item.TypeVOUCHER:    true,
		item.TypeGOODS:      false,
		item.TypeINGREDIENT: false,
		item.TypeEQUIPMENT:  false,
		item.TypeRECIPE:     false,
	}
	for typ, want := range cases {
		if got := holdsNoStock(&ent.Item{Type: typ}); got != want {
			t.Errorf("holdsNoStock(%s) = %v, want %v", typ, got, want)
		}
	}
	if holdsNoStock(nil) {
		t.Error("holdsNoStock(nil) must be false")
	}
}
