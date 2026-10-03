// Package stockcalc holds the pure stock arithmetic shared by the stock engine, the items
// read model and recipe costing: converting a recipe/sale quantity into an item's stock unit
// and computing how many portions of a recipe its ingredients can produce. It depends only on
// ent entities and the units table, so packages that cannot import each other (stock imports
// items) can both use one implementation instead of drifting copies.
package stockcalc

import (
	"math"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
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

// ProduciblePortions returns how many whole portions of a recipe can be produced from the
// given ingredient availability (keyed by ingredient item ID, in stock units): the minimum
// over constraining lines of floor(available / per-portion need). constrains reports whether
// an ingredient item participates (non-depleting items never do). Lines that never deduct are
// skipped. A missing or non-positive balance on any constraining line yields 0, as does a
// recipe with no constraining line at all. Ingredient Item edges (with Units) must be loaded.
func ProduciblePortions(r *ent.Recipe, available map[uuid.UUID]float64, constrains func(*ent.Item) bool) float64 {
	if r == nil || len(r.Edges.Ingredients) == 0 {
		return 0
	}
	min := -1.0
	for _, ing := range r.Edges.Ingredients {
		if constrains != nil && !constrains(ing.Edges.Item) {
			continue
		}
		need, ok := PerPortionStockQty(ing, r.OutputQty)
		if !ok {
			continue
		}
		avail, has := available[ing.ItemID]
		if !has || avail <= 0 {
			return 0
		}
		portions := math.Floor(avail / need)
		if min < 0 || portions < min {
			min = portions
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// AllIngredientsAvailable reports whether every constraining, deducting line of a recipe has
// positive availability. Same skip rules as ProduciblePortions.
func AllIngredientsAvailable(r *ent.Recipe, available map[uuid.UUID]float64, constrains func(*ent.Item) bool) bool {
	if r == nil {
		return false
	}
	for _, ing := range r.Edges.Ingredients {
		if constrains != nil && !constrains(ing.Edges.Item) {
			continue
		}
		if _, ok := PerPortionStockQty(ing, r.OutputQty); !ok {
			continue
		}
		if avail, has := available[ing.ItemID]; !has || avail <= 0 {
			return false
		}
	}
	return true
}
