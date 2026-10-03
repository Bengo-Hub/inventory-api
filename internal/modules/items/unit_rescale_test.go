package items

import (
	"math"
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func fp(v float64) *float64 { return &v }

// TestRescaleDTOGramsToKilograms is the user's example: an ingredient bought at 450 per 1000 g,
// costed 0.45/g, switched from g to kg must become 450 per 1 kg at 450/kg, not keep 0.45.
func TestRescaleDTOGramsToKilograms(t *testing.T) {
	prev := &ent.Item{
		Type:             item.TypeINGREDIENT,
		CostPrice:        fp(0.45),
		PurchasePrice:    fp(450),
		PurchasePackSize: fp(1000),
		PurchaseUnit:     "1000 g",
	}
	r := &unitRescale{From: &ent.Unit{Abbreviation: "g"}, To: &ent.Unit{Abbreviation: "kg"}, Factor: 0.001}

	// The edit form echoes the stored values back unchanged.
	dto := ItemDTO{CostPrice: fp(0.45), PurchasePrice: fp(450), PurchasePackSize: fp(1000), PurchaseUnit: "1000 g"}
	rescaleDTOForUnitChange(prev, &dto, r)

	if !near(*dto.CostPrice, 450) {
		t.Errorf("cost per stock unit = %v, want 450/kg", *dto.CostPrice)
	}
	if !near(*dto.PurchasePrice, 450) {
		t.Errorf("purchase price = %v, want unchanged 450", *dto.PurchasePrice)
	}
	if !near(*dto.PurchasePackSize, 1) {
		t.Errorf("pack size = %v, want 1 kg", *dto.PurchasePackSize)
	}
	if dto.PurchaseUnit != "1 kg" {
		t.Errorf("purchase unit = %q, want %q", dto.PurchaseUnit, "1 kg")
	}
}

// TestRescaleDTOKeepsEditedValues: a value the client changed in the same request is already in
// the new unit and must not be converted again.
func TestRescaleDTOKeepsEditedValues(t *testing.T) {
	prev := &ent.Item{Type: item.TypeGOODS, CostPrice: fp(0.45), MaxSellingPrice: fp(1)}
	r := &unitRescale{From: &ent.Unit{Abbreviation: "g"}, To: &ent.Unit{Abbreviation: "kg"}, Factor: 0.001}
	dto := ItemDTO{CostPrice: fp(500), MaxSellingPrice: fp(1)}
	rescaleDTOForUnitChange(prev, &dto, r)
	if !near(*dto.CostPrice, 500) {
		t.Errorf("edited cost = %v, want 500 untouched", *dto.CostPrice)
	}
	if !near(*dto.MaxSellingPrice, 1000) {
		t.Errorf("unchanged max price = %v, want 1000/kg", *dto.MaxSellingPrice)
	}
}

// TestRescaleRecipeKeepsSellingPrice: a recipe is priced per portion, never per stock unit.
func TestRescaleRecipeKeepsSellingPrice(t *testing.T) {
	prev := &ent.Item{Type: item.TypeRECIPE, MaxSellingPrice: fp(900)}
	r := &unitRescale{From: &ent.Unit{Abbreviation: "g"}, To: &ent.Unit{Abbreviation: "kg"}, Factor: 0.001}
	dto := ItemDTO{MaxSellingPrice: fp(900)}
	rescaleDTOForUnitChange(prev, &dto, r)
	if !near(*dto.MaxSellingPrice, 900) {
		t.Errorf("recipe price = %v, want 900", *dto.MaxSellingPrice)
	}
}

func TestRewritePurchaseUnit(t *testing.T) {
	cases := []struct {
		label, from, to string
		factor          float64
		want            string
	}{
		{"1000 g", "g", "kg", 0.001, "1 kg"},
		{"5 kg bag", "kg", "g", 1000, "5000 g bag"},
		{"g", "g", "kg", 0.001, "0.001 kg"},
		{"400 ml tin", "ml", "L", 0.001, "0.4 L tin"},
		{"crate", "g", "kg", 0.001, "crate"},
	}
	for _, c := range cases {
		if got := rewritePurchaseUnit(c.label, c.from, c.to, c.factor); got != c.want {
			t.Errorf("rewritePurchaseUnit(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

func TestScaleReorderNeverDropsToZero(t *testing.T) {
	if got := scaleReorder(500, 0.001); got != 1 {
		t.Errorf("500 g reorder in kg = %d, want 1 (never 0)", got)
	}
	if got := scaleReorder(2, 1000); got != 2000 {
		t.Errorf("2 kg reorder in g = %d, want 2000", got)
	}
	if got := scaleReorder(0, 1000); got != 0 {
		t.Errorf("unset reorder must stay 0, got %d", got)
	}
}

func TestStockUnitChangeErrorMessage(t *testing.T) {
	e := &StockUnitChangeError{From: "g", To: "pc"}
	if e.Error() == "" {
		t.Fatal("empty message")
	}
}
