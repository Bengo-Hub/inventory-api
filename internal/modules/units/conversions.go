package units

import "fmt"

// stdConversions maps (from_unit, to_unit) → multiplication factor.
// All values are lowercase. Conversions are one-directional; the reverse is computed on demand.
var stdConversions = map[[2]string]float64{
	// Volume
	{"tsp", "ml"}:   5.0,
	{"tbsp", "ml"}:  15.0,
	{"cup", "ml"}:   240.0,
	{"floz", "ml"}:  29.5735,
	{"pint", "ml"}:  473.176,
	{"litre", "ml"}: 1000.0,
	{"liter", "ml"}: 1000.0,
	{"l", "ml"}:     1000.0,
	{"dl", "ml"}:    100.0,
	{"cl", "ml"}:    10.0,
	// Weight
	{"oz", "g"}:  28.3495,
	{"lb", "g"}:  453.592,
	{"kg", "g"}:  1000.0,
	// Count / portion
	{"dozen", "pc"}: 12.0,
	{"pair", "pc"}:  2.0,
	{"tray", "pc"}:  30.0, // standard egg tray
	// Common cooking conversions (approximations for costing)
	{"clove", "g"}: 3.0,  // garlic clove ≈ 3g
	{"slice", "g"}: 20.0, // bread slice ≈ 20g
	{"leaf", "g"}:  0.5,  // bay leaf ≈ 0.5g
	{"sprig", "g"}: 2.0,  // herb sprig ≈ 2g
	{"pinch", "g"}: 0.5,  // pinch of salt ≈ 0.5g
	{"dash", "ml"}: 0.6,  // cocktail dash ≈ 0.6ml
	{"tot", "ml"}:  25.0, // spirits tot (UK/Kenya) ≈ 25ml
	{"shot", "ml"}: 30.0, // espresso / spirits shot ≈ 30ml
}

// ToBase converts qty from fromUnit to toUnit.
// Both unit strings are normalised to lowercase before lookup.
// Returns an error when no conversion is registered for the pair.
func ToBase(qty float64, fromUnit, toUnit string) (float64, error) {
	if fromUnit == toUnit {
		return qty, nil
	}
	f := normalize(fromUnit)
	t := normalize(toUnit)
	if f == t {
		return qty, nil
	}
	// Direct lookup
	if factor, ok := stdConversions[[2]string{f, t}]; ok {
		return qty * factor, nil
	}
	// Reverse lookup
	if factor, ok := stdConversions[[2]string{t, f}]; ok {
		return qty / factor, nil
	}
	return 0, fmt.Errorf("units: no conversion registered from %q to %q", fromUnit, toUnit)
}

// SupportedConversions returns all registered (from, to) unit pairs.
// Useful for documentation and validation UIs.
func SupportedConversions() [][2]string {
	pairs := make([][2]string, 0, len(stdConversions)*2)
	for pair := range stdConversions {
		pairs = append(pairs, pair)
		pairs = append(pairs, [2]string{pair[1], pair[0]}) // reverse also works
	}
	return pairs
}

func normalize(u string) string {
	result := make([]byte, 0, len(u))
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 'A' && c <= 'Z' {
			result = append(result, c+32)
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}
