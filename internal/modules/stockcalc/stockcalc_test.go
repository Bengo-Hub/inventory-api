package stockcalc

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

func unitItem(id uuid.UUID, abbr string, contentQty float64, contentUOM string) *ent.Item {
	itm := &ent.Item{ID: id}
	itm.Edges.Units = &ent.Unit{Abbreviation: abbr, Name: abbr}
	if contentQty > 0 {
		itm.UnitContentQty = &contentQty
		itm.UnitContentUom = contentUOM
	}
	return itm
}

func line(itm *ent.Item, qty float64, uom string, waste float64) *ent.RecipeIngredient {
	ri := &ent.RecipeIngredient{ItemID: itm.ID, Quantity: qty, UnitOfMeasure: uom, WastePercent: waste}
	ri.Edges.Item = itm
	return ri
}

// TestProduciblePortions covers the shared portion maths: unit conversion (g line against a kg
// stock), the content-per-unit bridge (a 30 ml tot from a 750 ml bottle stocked in btl), waste,
// a negative balance, and an unconvertible line that must never constrain.
func TestProduciblePortions(t *testing.T) {
	chicken := unitItem(uuid.New(), "kg", 0, "")
	gin := unitItem(uuid.New(), "btl", 750, "ml")
	tilapia := unitItem(uuid.New(), "pc", 0, "") // no content bridge: a g line can't convert

	r := &ent.Recipe{OutputQty: 1}
	r.Edges.Ingredients = []*ent.RecipeIngredient{
		line(chicken, 150, "g", 0),  // 0.15 kg per portion
		line(gin, 30, "ml", 0),      // 0.04 btl per portion
		line(tilapia, 400, "g", 10), // unconvertible: skipped
	}
	avail := map[uuid.UUID]float64{chicken.ID: 1.5, gin.ID: 0.5} // 10 portions vs 12 portions
	if got := ProduciblePortions(r, avail, nil); got != 10 {
		t.Fatalf("portions = %v, want 10", got)
	}
	if !AllIngredientsAvailable(r, avail, nil) {
		t.Fatal("all deducting lines are positive, want available")
	}

	avail[chicken.ID] = -2 // oversold ingredient
	if got := ProduciblePortions(r, avail, nil); got != 0 {
		t.Fatalf("negative ingredient: portions = %v, want 0", got)
	}
	if AllIngredientsAvailable(r, avail, nil) {
		t.Fatal("negative ingredient must report unavailable")
	}

	// A non-constraining (non-depleting) ingredient is ignored entirely.
	skipChicken := func(itm *ent.Item) bool { return itm.ID != chicken.ID }
	if got := ProduciblePortions(r, avail, skipChicken); got != 12 {
		t.Fatalf("non-constraining chicken: portions = %v, want 12", got)
	}
}

// TestPerPortionIncludesWaste pins that availability uses the same waste-inclusive quantity
// the deduction path removes.
func TestPerPortionIncludesWaste(t *testing.T) {
	flour := unitItem(uuid.New(), "g", 0, "")
	qty, ok := PerPortionStockQty(line(flour, 100, "g", 10), 2)
	if !ok || qty < 54.9999 || qty > 55.0001 {
		t.Fatalf("got (%v,%v), want (55,true)", qty, ok)
	}
}
