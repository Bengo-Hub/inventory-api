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
	if got := ProduciblePortions(r, avail, nil, nil); got != 10 {
		t.Fatalf("portions = %v, want 10", got)
	}
	if !AllIngredientsAvailable(r, avail, nil, nil) {
		t.Fatal("all deducting lines are positive, want available")
	}

	avail[chicken.ID] = -2 // oversold ingredient
	if got := ProduciblePortions(r, avail, nil, nil); got != 0 {
		t.Fatalf("negative ingredient: portions = %v, want 0", got)
	}
	if AllIngredientsAvailable(r, avail, nil, nil) {
		t.Fatal("negative ingredient must report unavailable")
	}

	// A non-constraining (non-depleting) ingredient is ignored entirely.
	skipChicken := func(itm *ent.Item) bool { return itm.ID != chicken.ID }
	if got := ProduciblePortions(r, avail, skipChicken, nil); got != 12 {
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

func recipeOf(output float64, lines ...*ent.RecipeIngredient) *ent.Recipe {
	r := &ent.Recipe{ID: uuid.New(), OutputQty: output}
	r.Edges.Ingredients = lines
	return r
}

// viaSub marks a line as riding a sub-recipe, the link autoLinkSubRecipes writes.
func viaSub(ri *ent.RecipeIngredient, sub *ent.Recipe) *ent.RecipeIngredient {
	id := sub.ID
	ri.SubRecipeID = &id
	return ri
}

func subsOf(rs ...*ent.Recipe) SubRecipes {
	out := make(SubRecipes, len(rs))
	for _, r := range rs {
		out[r.ID] = r
	}
	return out
}

// TestSubRecipeResolution: a cocktail made from lemon juice (a prep recipe whose item has no
// stock of its own) and gin. The juice line must read what the lemons can make, not the juice
// item's missing balance, which used to give the cocktail 0 portions.
func TestSubRecipeResolution(t *testing.T) {
	lemon := unitItem(uuid.New(), "g", 0, "")
	juice := unitItem(uuid.New(), "ml", 0, "")
	gin := unitItem(uuid.New(), "ml", 0, "")

	juiceRecipe := recipeOf(500, line(lemon, 1000, "g", 0)) // 2 g of lemon per ml of juice
	cocktail := recipeOf(1, viaSub(line(juice, 50, "ml", 0), juiceRecipe), line(gin, 30, "ml", 0))
	subs := subsOf(juiceRecipe)

	avail := map[uuid.UUID]float64{lemon.ID: 3000, gin.ID: 600} // juice 1500 ml = 30 portions, gin 20
	if got := ProduciblePortions(cocktail, avail, nil, subs); got != 20 {
		t.Fatalf("portions = %v, want 20 (gin limits)", got)
	}
	if got := ProduciblePortions(cocktail, avail, nil, nil); got != 0 {
		t.Fatalf("without the sub-recipe loaded the juice item's own empty balance applies: got %v, want 0", got)
	}

	avail[lemon.ID] = 1500 // 750 ml of juice = 15 portions
	if got := ProduciblePortions(cocktail, avail, nil, subs); got != 15 {
		t.Fatalf("portions = %v, want 15 (lemons limit)", got)
	}
	if !AllIngredientsAvailable(cocktail, avail, nil, subs) {
		t.Fatal("lemons and gin in stock: want available")
	}

	// Prepared juice on hand is used first, exactly like the deduction path.
	avail[juice.ID] = 100
	if got := ProduciblePortions(cocktail, avail, nil, subs); got != 2 {
		t.Fatalf("prepared juice 100 ml: portions = %v, want 2", got)
	}

	delete(avail, juice.ID)
	avail[lemon.ID] = 0
	if got := ProduciblePortions(cocktail, avail, nil, subs); got != 0 {
		t.Fatalf("no lemons and no juice: portions = %v, want 0", got)
	}
	if AllIngredientsAvailable(cocktail, avail, nil, subs) {
		t.Fatal("no lemons and no juice: want unavailable")
	}
}

// TestSubRecipeCycleGuard: A uses B and B uses A. The second visit to A must read A's item
// balance instead of recursing forever.
func TestSubRecipeCycleGuard(t *testing.T) {
	aItem := unitItem(uuid.New(), "pc", 0, "")
	bItem := unitItem(uuid.New(), "pc", 0, "")
	flour := unitItem(uuid.New(), "pc", 0, "")

	a := recipeOf(1)
	b := recipeOf(1)
	a.Edges.Ingredients = []*ent.RecipeIngredient{viaSub(line(bItem, 1, "pc", 0), b)}
	b.Edges.Ingredients = []*ent.RecipeIngredient{line(flour, 1, "pc", 0), viaSub(line(aItem, 1, "pc", 0), a)}
	subs := subsOf(a, b)

	avail := map[uuid.UUID]float64{flour.ID: 7, aItem.ID: 3}
	if got := ProduciblePortions(a, avail, nil, subs); got != 3 {
		t.Fatalf("portions = %v, want 3 (the cyclic line reads A's own 3)", got)
	}
	if !AllIngredientsAvailable(a, avail, nil, subs) {
		t.Fatal("want available")
	}
}

// TestSubRecipeDepthLimit: a chain deeper than MaxSubRecipeDepth stops expanding at the cap and
// reads the prepared item's own balance there.
func TestSubRecipeDepthLimit(t *testing.T) {
	flour := unitItem(uuid.New(), "pc", 0, "")
	// chain builds a top recipe with the given number of sub-recipe levels under it; the
	// bottom one uses flour. No prepared item along the way holds stock of its own.
	chain := func(levels int) (*ent.Recipe, SubRecipes) {
		cur := recipeOf(1, line(flour, 1, "pc", 0))
		all := []*ent.Recipe{cur}
		for i := 0; i < levels; i++ {
			made := unitItem(uuid.New(), "pc", 0, "")
			cur = recipeOf(1, viaSub(line(made, 1, "pc", 0), cur))
			all = append(all, cur)
		}
		return cur, subsOf(all...)
	}

	// MaxSubRecipeDepth levels of sub-recipes under the top: the bottom flour line is reached.
	top, subs := chain(MaxSubRecipeDepth)
	if got := ProduciblePortions(top, map[uuid.UUID]float64{flour.ID: 100}, nil, subs); got != 100 {
		t.Fatalf("chain within the cap: portions = %v, want 100", got)
	}

	// One level more: the deepest sub-recipe line is past the cap and reads its item's own
	// balance (none here), so nothing can be made even with flour in stock.
	top, subs = chain(MaxSubRecipeDepth + 1)
	if got := ProduciblePortions(top, map[uuid.UUID]float64{flour.ID: 100}, nil, subs); got != 0 {
		t.Fatalf("chain past the cap: portions = %v, want 0", got)
	}
}

// TestUnconstrainedRecipeIsUnlimited: a recipe whose lines never constrain stock (all
// non-depleting, or units that never convert) is available without limit, not 0.
func TestUnconstrainedRecipeIsUnlimited(t *testing.T) {
	tea := unitItem(uuid.New(), "g", 0, "")
	water := unitItem(uuid.New(), "pc", 0, "") // a ml line against pieces with no bridge never deducts
	r := recipeOf(1, line(tea, 5, "g", 0), line(water, 300, "ml", 0))
	never := func(*ent.Item) bool { return false }

	if got := ProduciblePortions(r, map[uuid.UUID]float64{}, never, nil); got != Unlimited {
		t.Fatalf("all non-depleting: portions = %v, want Unlimited", got)
	}
	if !AllIngredientsAvailable(r, map[uuid.UUID]float64{}, never, nil) {
		t.Fatal("all non-depleting: want available")
	}

	onlyMismatch := recipeOf(1, line(water, 300, "ml", 0))
	if got := ProduciblePortions(onlyMismatch, nil, nil, nil); got != Unlimited {
		t.Fatalf("only unit mismatches: portions = %v, want Unlimited", got)
	}

	// A sub-recipe line whose sub-recipe is itself unconstrained does not constrain either.
	syrup := unitItem(uuid.New(), "ml", 0, "")
	syrupRecipe := recipeOf(100, line(tea, 10, "g", 0))
	parent := recipeOf(1, viaSub(line(syrup, 20, "ml", 0), syrupRecipe))
	if got := ProduciblePortions(parent, map[uuid.UUID]float64{}, never, subsOf(syrupRecipe)); got != Unlimited {
		t.Fatalf("unconstrained sub-recipe: portions = %v, want Unlimited", got)
	}
}

// TestMixedConstrainingLines: non-depleting and unconvertible lines are ignored, the tracked
// line alone decides.
func TestMixedConstrainingLines(t *testing.T) {
	tea := unitItem(uuid.New(), "g", 0, "")
	milk := unitItem(uuid.New(), "ml", 0, "")
	water := unitItem(uuid.New(), "pc", 0, "")
	r := recipeOf(1, line(tea, 5, "g", 0), line(milk, 100, "ml", 0), line(water, 300, "ml", 0))
	onlyMilk := func(itm *ent.Item) bool { return itm.ID == milk.ID }

	if got := ProduciblePortions(r, map[uuid.UUID]float64{milk.ID: 450}, onlyMilk, nil); got != 4 {
		t.Fatalf("portions = %v, want 4 (milk alone constrains)", got)
	}
	if got := ProduciblePortions(r, map[uuid.UUID]float64{milk.ID: -20, tea.ID: 1000}, onlyMilk, nil); got != 0 {
		t.Fatalf("oversold milk: portions = %v, want 0", got)
	}
}
