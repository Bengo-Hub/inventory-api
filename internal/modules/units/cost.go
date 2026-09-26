package units

// Item.cost_price is always a cost per STOCK (base) unit: every valuation multiplies a base-unit
// quantity by it. For an item stocked in a measure unit (g, ml, kg, l, ...) a cost entered per
// PURCHASE unit (KES 350 per kg on an item stocked in grams) values stock 1000x too high, which on
// urban-loft pushed tens of millions of phantom inventory and wastage into the ledger (2026-09).
// These helpers are the one place that rule lives; the items service applies it on save and the
// stock module on valuation.

// PurchaseMismatchFactor is how far above the purchase-derived per-base-unit cost a cost may sit
// before it is treated as a per-purchase-unit entry. 5x leaves room for price moves and yield.
const PurchaseMismatchFactor = 5.0

// IsMeasure reports whether a unit is a mass or volume unit (convertible to g or ml), i.e. one a
// purchase unit can differ from by a scale factor. Count units (pc, btl, pack) are not.
func IsMeasure(abbr string) bool {
	if _, ok := Convert(1, abbr, "g"); ok {
		return true
	}
	_, ok := Convert(1, abbr, "ml")
	return ok
}

// PurchaseCostPerBaseUnit is purchase_price / pack_size / yield (the edible-portion cost per base
// unit); ok is false when the purchase price or pack size is missing or not positive.
func PurchaseCostPerBaseUnit(purchasePrice, packSize, yieldPct *float64) (float64, bool) {
	if purchasePrice == nil || packSize == nil || *purchasePrice <= 0 || *packSize <= 0 {
		return 0, false
	}
	y := 1.0
	if yieldPct != nil && *yieldPct > 0 && *yieldPct <= 1 {
		y = *yieldPct
	}
	return *purchasePrice / *packSize / y, true
}

// CostPerBaseUnit returns the cost to use per stock unit. For a measure-stocked item whose cost is
// more than PurchaseMismatchFactor times the purchase-derived cost, the purchase-derived cost is
// returned (the stored cost is per purchase unit); otherwise the cost as given. Count-stocked items
// are never rescaled: there a gap means wrong purchase data, not a unit mix-up.
func CostPerBaseUnit(cost float64, purchasePrice, packSize, yieldPct *float64, stockUnit string) float64 {
	if !IsMeasure(stockUnit) {
		return cost
	}
	if ep, ok := PurchaseCostPerBaseUnit(purchasePrice, packSize, yieldPct); ok && cost > ep*PurchaseMismatchFactor {
		return ep
	}
	return cost
}
