package units

import "strings"

// This file provides best-effort quantity conversion between common measurement
// units used in recipes/BOMs (mass, volume, count). Tenant Unit rows carry no
// conversion factors, so conversion is driven by a built-in table keyed on the
// canonical abbreviation. It lets a recipe express an ingredient in a smaller
// unit (e.g. 2.5 ml of an oil stocked in litres) and resolve it back to the
// ingredient's base unit before it is stored/costed.

// dimension groups units that can convert between one another.
type dimension string

const (
	dimMass    dimension = "mass"
	dimVolume  dimension = "volume"
	dimCount   dimension = "count"
	dimUnknown dimension = ""
)

// unitFactor is the number of canonical base units in one of this unit
// (mass canonical = gram, volume canonical = millilitre, count canonical = piece).
type unitFactor struct {
	dim    dimension
	factor float64
}

// unitTable maps a normalised abbreviation to its dimension + factor.
var unitTable = map[string]unitFactor{
	// mass (base: g)
	"mg": {dimMass, 0.001},
	"g":  {dimMass, 1},
	"kg": {dimMass, 1000},
	"oz": {dimMass, 28.349523125},
	"lb": {dimMass, 453.59237},
	"t":  {dimMass, 1_000_000},

	// volume (base: ml)
	"ml":   {dimVolume, 1},
	"cl":   {dimVolume, 10},
	"dl":   {dimVolume, 100},
	"l":    {dimVolume, 1000},
	"tsp":  {dimVolume, 5},
	"tbsp": {dimVolume, 15},
	"cup":  {dimVolume, 240},
	"floz": {dimVolume, 29.5735},
	"pt":   {dimVolume, 473.176},
	"qt":   {dimVolume, 946.353},
	"gal":  {dimVolume, 3785.41},

	// count (base: pc)
	"pc":  {dimCount, 1},
	"doz": {dimCount, 12},
}

// synonyms maps free-text unit spellings to a canonical abbreviation.
var synonyms = map[string]string{
	"milligram": "mg", "milligrams": "mg",
	"gram": "g", "grams": "g", "gm": "g", "gr": "g",
	"kilogram": "kg", "kilograms": "kg", "kilo": "kg", "kgs": "kg",
	"ounce": "oz", "ounces": "oz",
	"pound": "lb", "pounds": "lb", "lbs": "lb",
	"tonne": "t", "ton": "t", "tonnes": "t",

	"milliliter": "ml", "millilitre": "ml", "milliliters": "ml", "millilitres": "ml",
	"centiliter": "cl", "centilitre": "cl",
	"deciliter": "dl", "decilitre": "dl",
	"liter": "l", "litre": "l", "liters": "l", "litres": "l", "lt": "l", "ltr": "l",
	"teaspoon": "tsp", "teaspoons": "tsp", "tsps": "tsp",
	"tablespoon": "tbsp", "tablespoons": "tbsp", "tbsps": "tbsp", "tbs": "tbsp",
	"cups": "cup",
	"fluidounce": "floz", "fl oz": "floz", "fluid ounce": "floz",
	"pint": "pt", "pints": "pt",
	"quart": "qt", "quarts": "qt",
	"gallon": "gal", "gallons": "gal",

	"piece": "pc", "pieces": "pc", "pcs": "pc", "each": "pc", "ea": "pc",
	"unit": "pc", "units": "pc", "item": "pc", "items": "pc",
	"dozen": "doz", "dozens": "doz",
}

// NormalizeUnit lower-cases, trims and maps a unit spelling to its canonical
// abbreviation. Unknown units are returned lower-cased/trimmed unchanged.
func NormalizeUnit(u string) string {
	s := strings.ToLower(strings.TrimSpace(u))
	if s == "" {
		return ""
	}
	if canon, ok := synonyms[s]; ok {
		return canon
	}
	return s
}

// Convert returns qty expressed in the `to` unit and whether the conversion was
// possible. Same (normalised) unit → unchanged. Different units of the same
// dimension → scaled. Unknown or cross-dimension units → (qty, false), leaving
// the caller to treat it as already being in the target unit.
func Convert(qty float64, from, to string) (float64, bool) {
	nf, nt := NormalizeUnit(from), NormalizeUnit(to)
	if nf == nt || nf == "" || nt == "" {
		return qty, nf == nt && nf != ""
	}
	ff, okF := unitTable[nf]
	ft, okT := unitTable[nt]
	if !okF || !okT || ff.dim != ft.dim {
		return qty, false
	}
	return qty * ff.factor / ft.factor, true
}
