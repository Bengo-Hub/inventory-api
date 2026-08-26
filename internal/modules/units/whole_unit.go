package units

import (
	"fmt"
	"math"
)

// IsWholeUnitOnly reports whether a Unit's type represents a discrete, countable thing
// (pieces, boxes, tablets, bottles, ...) that can never legitimately carry a fractional
// quantity — as opposed to a continuous measure (weight, volume, length, area, time) where a
// decimal is normal and expected. Driven by the Unit.type free-text classification already
// seeded for exactly this purpose (cmd/seed/seed_units.go): "count" is the only value that
// implies discreteness. An unset/legacy/unclassified type is treated as fractional-permitted
// so an incompletely-categorised unit (e.g. auto-created on a bulk import) never falsely
// blocks a legitimate entry.
func IsWholeUnitOnly(unitType string) bool {
	return unitType == "count"
}

// ValidateQuantityForUnit rejects a non-integer quantity when unitType is whole-unit-only
// (see IsWholeUnitOnly) — e.g. "4427.67 phones" or "0.33 boxes" is never a valid stock
// quantity. unitName is used only to name the unit in the error message.
//
// hasContentBridge is true when the item has Item.unit_content_qty set (e.g. a 50ml bottle
// stocked in pieces/bottles but also decanted/refilled by the ml via a linked recipe or "tot")
// — for that item the count-based unit no longer implies "always whole": once a bottle is
// partially consumed, 1.41 bottles is exactly how much remains, not a data error. The
// whole-unit rule is skipped entirely for these items, regardless of unitType.
func ValidateQuantityForUnit(qty float64, unitType, unitName string, hasContentBridge bool) error {
	if hasContentBridge || !IsWholeUnitOnly(unitType) {
		return nil
	}
	if qty != math.Trunc(qty) {
		if unitName == "" {
			unitName = "this item's unit"
		}
		return fmt.Errorf("quantity must be a whole number for %s (a count-based unit) — got %g", unitName, qty)
	}
	return nil
}
