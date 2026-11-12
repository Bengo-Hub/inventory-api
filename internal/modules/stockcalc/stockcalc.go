// Package stockcalc holds the pure stock arithmetic shared by the stock engine, the items
// read model and recipe costing: converting a recipe/sale quantity into an item's stock unit
// and computing how many portions of a recipe its ingredients can produce. It depends only on
// ent entities and the units table, so packages that cannot import each other (stock imports
// items) can both use one implementation instead of drifting copies.
package stockcalc

import (
	"context"
	"math"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// NonDepleting reports whether sales must NOT decrement this item's stock.
// Explicit per-item mode always wins; items left on "default" follow the tenant's
// recipe_items_non_depleting_default policy, but only RECIPE-type items (goods,
// ingredients and bottles keep depleting so easy-to-track stock stays accurate).
func NonDepleting(itm *ent.Item, cfg *ent.TenantInventoryConfig) bool {
	if itm == nil {
		return false
	}
	switch itm.StockTrackingMode {
	case item.StockTrackingModeNonDepleting:
		return true
	case item.StockTrackingModeTracked:
		return false
	}
	return itm.Type == item.TypeRECIPE && cfg != nil && cfg.RecipeItemsNonDepletingDefault
}

// ConvertToStockUnit converts a quantity expressed in a recipe-line/sale unit into the
// item's stock (base) unit. Resolution order:
//  1. the line's unit is blank/unknown (no fromUOM supplied at all) — nothing to convert
//     against, preserve the historical raw-passthrough so pre-normalised rows (written by
//     the composite flow already in base units) keep working;
//  2. the line's unit IS the item's own stock unit — by abbreviation ("btl") OR by its
//     human-readable Name ("BOTTLE"), since recipe/sale lines are written with either
//     spelling depending on which picker wrote them — no conversion needed;
//  3. same-dimension unit conversion (ml→l, g→kg, …) via the built-in units table;
//  4. content-per-unit bridge for count-stocked packaged goods: a 30 ml line against a
//     750 ml-per-piece bottle deducts 30/750 = 0.04 pieces (cumulative tots deplete
//     whole bottles exactly);
//  5. cross-dimension with no bridge, OR the item carries no stock unit at all → ok=false:
//     the caller must NOT deduct raw. An item with no assigned stock unit is exactly the
//     unconfigured/ambiguous case this function exists to protect against — silently
//     treating "no unit" as "same unit" let an ml-denominated recipe line deduct 1:1 raw
//     units from a bulk-imported item that was never given a proper unit_id, instead of
//     refusing like a real cross-dimension mismatch does.
//
// The item's Units edge must be loaded.
func ConvertToStockUnit(itm *ent.Item, qty float64, fromUOM string) (float64, bool) {
	from := units.NormalizeUnit(fromUOM)
	stockUnit := ""
	stockUnitName := ""
	if itm != nil && itm.Edges.Units != nil {
		stockUnit = units.NormalizeUnit(itm.Edges.Units.Abbreviation)
		stockUnitName = units.NormalizeUnit(itm.Edges.Units.Name)
	}
	if from == "" {
		return qty, true
	}
	// A line written with the unit's display Name (e.g. a "BOTTLE" recipe/sale line
	// against an item stocked in "btl") is the SAME unit, not a cross-dimension mismatch —
	// the built-in conversion table only knows standard mass/volume/count spellings, never
	// a tenant's custom unit names (btl/gls/can/box/ptn/…), so it must never be asked to
	// judge a unit against itself under a different spelling.
	if from == stockUnit || (stockUnitName != "" && from == stockUnitName) {
		return qty, true
	}
	if stockUnit == "" {
		// The item has no assigned stock unit at all — there is nothing to safely judge
		// the line's unit against, so this must refuse exactly like an unbridgeable
		// cross-dimension mismatch does, not silently pass the raw quantity through.
		return qty, false
	}
	if converted, ok := units.Convert(qty, from, stockUnit); ok {
		return converted, true
	}
	// Content-per-unit bridge (pieces ↔ ml/g) for fixed-content packaged goods.
	if itm.UnitContentQty != nil && *itm.UnitContentQty > 0 && itm.UnitContentUom != "" {
		if inContent, ok := units.Convert(qty, from, itm.UnitContentUom); ok {
			return inContent / *itm.UnitContentQty, true
		}
	}
	return qty, false
}

// PerPortionStockQty returns how much of a recipe line's ingredient one portion consumes, in
// the ingredient's stock unit, including the line's waste factor (the same quantity the
// deduction path removes). ok=false for a line that never deducts (unconvertible unit or a
// zero/negative need), which therefore never constrains availability. The ingredient's Item
// edge (with Units) must be loaded.
func PerPortionStockQty(ing *ent.RecipeIngredient, outputQty float64) (float64, bool) {
	if outputQty <= 0 {
		outputQty = 1
	}
	perPortion := ing.Quantity * (1 + ing.WastePercent/100) / outputQty
	if perPortion <= 0 {
		return 0, false
	}
	stockQty, ok := ConvertToStockUnit(ing.Edges.Item, perPortion, ing.UnitOfMeasure)
	if !ok || stockQty <= 0 {
		return 0, false
	}
	return stockQty, true
}

// MaxSubRecipeDepth caps how deep sub-recipes nest, for the deduction path (stock.explodeBOM)
// and for availability alike, so both agree on what a sale removes. Past it, or on a cycle, a
// sub-recipe line reads the prepared item's own balance.
const MaxSubRecipeDepth = 5

// Unlimited is what ProduciblePortions returns when no ingredient line constrains stock: every
// line is non-depleting, never deducts (unit mismatch, zero quantity) or rides a sub-recipe that
// is itself unconstrained. Callers show it as available, never as out of stock.
const Unlimited = -1.0

// SubRecipes holds the active recipes that sub-recipe lines can expand into, keyed by recipe
// ID, each with its ingredient lines (Item edge with Units) loaded. LoadSubRecipes builds it.
// A nil map means sub-recipe lines read the prepared item's own balance only.
type SubRecipes map[uuid.UUID]*ent.Recipe

// ProduciblePortions returns how many whole portions of a recipe can be produced from the
// given availability (keyed by item ID, in stock units): the minimum over constraining lines
// of floor(available / per-portion need). constrains reports whether an ingredient item
// participates (non-depleting items never do). Lines that never deduct are skipped. A missing
// or non-positive balance on any constraining line yields 0.
//
// A sub-recipe line follows the deduction path in stock.explodeBOM: the prepared item's own
// stock when it has any, otherwise what the sub-recipe's ingredients can make, resolved
// recursively through subs (depth MaxSubRecipeDepth, cycle guarded).
//
// Returns Unlimited (-1) when no line constrains stock, and 0 for a nil recipe. Ingredient Item
// edges (with Units) must be loaded.
func ProduciblePortions(r *ent.Recipe, available map[uuid.UUID]float64, constrains func(*ent.Item) bool, subs SubRecipes) float64 {
	if r == nil {
		return 0
	}
	capacity, limited := recipeCapacity(r, available, constrains, subs, map[uuid.UUID]struct{}{r.ID: {}}, 0)
	if !limited {
		return Unlimited
	}
	return math.Floor(capacity)
}

// AllIngredientsAvailable reports whether every constraining, deducting line of a recipe has
// positive availability, with sub-recipe lines resolved as in ProduciblePortions. A recipe
// with no constraining line is available.
func AllIngredientsAvailable(r *ent.Recipe, available map[uuid.UUID]float64, constrains func(*ent.Item) bool, subs SubRecipes) bool {
	if r == nil {
		return false
	}
	capacity, limited := recipeCapacity(r, available, constrains, subs, map[uuid.UUID]struct{}{r.ID: {}}, 0)
	return !limited || capacity > 0
}

// recipeCapacity is how many output units of r its lines can make, unfloored (a sub-recipe's
// output unit is the prepared item's stock unit, so 0.4 of a kg batch is a real quantity).
// limited is false when no line constrains. path holds the recipes being expanded above r.
func recipeCapacity(r *ent.Recipe, available map[uuid.UUID]float64, constrains func(*ent.Item) bool, subs SubRecipes, path map[uuid.UUID]struct{}, depth int) (float64, bool) {
	capacity, limited := 0.0, false
	for _, ing := range r.Edges.Ingredients {
		need, ok := PerPortionStockQty(ing, r.OutputQty)
		if !ok {
			continue
		}
		have, lineLimited := lineAvailable(ing, available, constrains, subs, path, depth)
		if !lineLimited {
			continue
		}
		if have <= 0 {
			return 0, true
		}
		if c := have / need; !limited || c < capacity {
			capacity, limited = c, true
		}
	}
	return capacity, limited
}

// lineAvailable is how much of a line's item can be had, in that item's stock unit. limited is
// false when the line never constrains.
func lineAvailable(ing *ent.RecipeIngredient, available map[uuid.UUID]float64, constrains func(*ent.Item) bool, subs SubRecipes, path map[uuid.UUID]struct{}, depth int) (float64, bool) {
	own, has := available[ing.ItemID]
	if sub := expandableSubRecipe(ing, subs, path, depth); sub != nil && (!has || own <= 0) {
		path[sub.ID] = struct{}{}
		capacity, limited := recipeCapacity(sub, available, constrains, subs, path, depth+1)
		delete(path, sub.ID)
		return capacity, limited
	}
	if constrains != nil && !constrains(ing.Edges.Item) {
		return 0, false
	}
	return own, true
}

// expandableSubRecipe returns the sub-recipe a line backflushes into, or nil when the line has
// none, it is not loaded or has no lines, the depth cap is reached, or it is already being
// expanded higher up (a cycle).
func expandableSubRecipe(ing *ent.RecipeIngredient, subs SubRecipes, path map[uuid.UUID]struct{}, depth int) *ent.Recipe {
	if ing.SubRecipeID == nil || depth >= MaxSubRecipeDepth {
		return nil
	}
	sub := subs[*ing.SubRecipeID]
	if sub == nil || len(sub.Edges.Ingredients) == 0 {
		return nil
	}
	if _, onPath := path[sub.ID]; onPath {
		return nil
	}
	return sub
}

// LoadSubRecipes loads every active sub-recipe the given recipes can expand into, level by
// level up to MaxSubRecipeDepth: one query per nesting level, whatever the number of recipes.
// The roots themselves are included, so a root used as another root's sub-recipe expands too.
func LoadSubRecipes(ctx context.Context, client *ent.Client, tenantID uuid.UUID, roots []*ent.Recipe) (SubRecipes, error) {
	subs := make(SubRecipes, len(roots))
	tried := make(map[uuid.UUID]struct{}, len(roots))
	for _, r := range roots {
		subs[r.ID] = r
		tried[r.ID] = struct{}{}
	}
	level := roots
	for depth := 0; depth < MaxSubRecipeDepth; depth++ {
		pending := make([]uuid.UUID, 0)
		for _, r := range level {
			for _, ing := range r.Edges.Ingredients {
				if ing.SubRecipeID == nil {
					continue
				}
				if _, done := tried[*ing.SubRecipeID]; done {
					continue
				}
				tried[*ing.SubRecipeID] = struct{}{}
				pending = append(pending, *ing.SubRecipeID)
			}
		}
		if len(pending) == 0 {
			break
		}
		next, err := client.Recipe.Query().
			Where(recipe.TenantID(tenantID), recipe.IsActive(true), recipe.IDIn(pending...)).
			WithIngredients(func(q *ent.RecipeIngredientQuery) {
				q.WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() })
			}).
			All(ctx)
		if err != nil {
			return subs, err
		}
		for _, r := range next {
			subs[r.ID] = r
		}
		level = next
	}
	return subs, nil
}

// IngredientItemIDs lists, once each, the item IDs on every ingredient line of the roots and
// the loaded sub-recipes: the balances availability needs.
func IngredientItemIDs(roots []*ent.Recipe, subs SubRecipes) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	add := func(r *ent.Recipe) {
		for _, ing := range r.Edges.Ingredients {
			if _, ok := seen[ing.ItemID]; !ok {
				seen[ing.ItemID] = struct{}{}
				ids = append(ids, ing.ItemID)
			}
		}
	}
	for _, r := range roots {
		add(r)
	}
	for _, r := range subs {
		add(r)
	}
	return ids
}
