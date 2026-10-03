package stock

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/modules/tenantconfig"
)

// TestReservationHold pins the manual-only availability rule: with oversell allowed a
// reservation holds the full request even past available stock (so an online order is never
// rejected for a system/physical mismatch); without it the hold is capped and flagged partial.
func TestReservationHold(t *testing.T) {
	cases := []struct {
		name             string
		requested, avail float64
		oversell         bool
		wantQty          float64
		wantFull         bool
	}{
		{"enough stock", 2, 5, false, 2, true},
		{"short, capped", 5, 2, false, 2, false},
		{"already negative, capped at zero", 3, -4, false, 0, false},
		{"short, oversell holds all", 5, 2, true, 5, true},
		{"negative, oversell holds all", 3, -4, true, 3, true},
	}
	for _, c := range cases {
		qty, full := reservationHold(c.requested, c.avail, c.oversell)
		if qty != c.wantQty || full != c.wantFull {
			t.Errorf("%s: got (%v,%v), want (%v,%v)", c.name, qty, full, c.wantQty, c.wantFull)
		}
	}
}

// TestAutoHideDefaultsOff pins the platform default: no config row, or a row left on its
// default, means availability is manual-only.
func TestAutoHideDefaultsOff(t *testing.T) {
	if tenantconfig.AutoHideOnStockOut(nil) {
		t.Error("nil config must mean manual-only availability")
	}
	if tenantconfig.AutoHideOnStockOut(&ent.TenantInventoryConfig{}) {
		t.Error("default config must mean manual-only availability")
	}
	if !tenantconfig.AutoHideOnStockOut(&ent.TenantInventoryConfig{AutoHideOnStockOut: true}) {
		t.Error("opted-in config must auto-hide")
	}
}

// TestCompositeLineTagShortCircuits pins that a tagged recipe summary line is recognised
// without touching the database (tx is never used on this path).
func TestCompositeLineTagShortCircuits(t *testing.T) {
	s := &Service{}
	if !s.isCompositeReservationLine(nil, nil, [16]byte{}, entschema.ReservedItemJSON{Composite: true}, nil) {
		t.Error("a Composite-tagged line must be skipped by release/consume")
	}
	if s.isCompositeReservationLine(nil, nil, [16]byte{}, entschema.ReservedItemJSON{}, &ent.Item{Type: "GOODS"}) {
		t.Error("a plain GOODS line must still move stock")
	}
}
