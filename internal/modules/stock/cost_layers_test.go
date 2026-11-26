package stock

import (
	"testing"

	"github.com/google/uuid"
)

func fp(v float64) *float64 { return &v }

func TestBaseAndWarehouseLotNumber(t *testing.T) {
	wh := uuid.MustParse("abcdef12-0000-0000-0000-000000000000")
	other := uuid.MustParse("12345678-0000-0000-0000-000000000000")
	if got := baseLotNumber("LOT-1"); got != "LOT-1" {
		t.Fatalf("base of plain = %q", got)
	}
	if got := baseLotNumber("LOT-1@12345678"); got != "LOT-1" {
		t.Fatalf("base of moved = %q", got)
	}
	if got := warehouseLotNumber("LOT-1", wh); got != "LOT-1@abcdef12" {
		t.Fatalf("warehouse name = %q", got)
	}
	// A lot moved on again never grows a second suffix.
	if got := warehouseLotNumber(warehouseLotNumber("LOT-1", other), wh); got != "LOT-1@abcdef12" {
		t.Fatalf("second hop name = %q", got)
	}
}

func TestBlendUnitCost(t *testing.T) {
	cases := []struct {
		name     string
		haveQty  float64
		haveCost *float64
		addQty   float64
		addCost  *float64
		want     *float64
	}{
		{"both nil", 5, nil, 5, nil, nil},
		{"weighted", 10, fp(100), 10, fp(200), fp(150)},
		{"depleted takes incoming", 0, fp(80), 4, fp(120), fp(120)},
		{"unpriced takes incoming", 3, nil, 2, fp(50), fp(50)},
		{"incoming unpriced keeps have", 3, fp(40), 2, nil, fp(40)},
		{"zero add keeps have", 3, fp(40), 0, fp(90), fp(40)},
		{"rounds to 4dp", 1, fp(1), 2, fp(2), fp(1.6667)},
	}
	for _, c := range cases {
		got := blendUnitCost(c.haveQty, c.haveCost, c.addQty, c.addCost)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %v, want nil", c.name, *got)
		case c.want != nil && (got == nil || *got != *c.want):
			t.Errorf("%s: got %v, want %v", c.name, got, *c.want)
		}
	}
}

func TestPositiveGain(t *testing.T) {
	cases := []struct{ before, after, want float64 }{
		{0, 5, 5},
		{2, 7, 5},
		{-3, 5, 5 - 0}, // only the part above zero is on the shelf
		{-3, -1, 0},    // paying down debt adds nothing physical
		{5, 3, 0},
		{-2, 2, 2},
	}
	for _, c := range cases {
		if got := positiveGain(c.before, c.after); got != c.want {
			t.Errorf("positiveGain(%v, %v) = %v, want %v", c.before, c.after, got, c.want)
		}
	}
}

func TestAllocateLanded(t *testing.T) {
	drawn := []LotConsumption{{QtyTaken: 3}, {QtyTaken: 4}, {QtyTaken: 2}}
	got := allocateLanded(drawn, 6)
	want := []float64{3, 3, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocateLanded short = %v, want %v", got, want)
		}
	}
	got = allocateLanded(drawn, 20)
	want = []float64{3, 4, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocateLanded over = %v, want %v", got, want)
		}
	}
	if got := allocateLanded(nil, 5); len(got) != 0 {
		t.Fatalf("no draws should allocate nothing, got %v", got)
	}
}
