package units

import "testing"

func TestConvert(t *testing.T) {
	cases := []struct {
		qty      float64
		from, to string
		want     float64
		ok       bool
	}{
		{2.5, "ml", "L", 0.0025, true},   // teaspoon of oil stocked in litres
		{1, "L", "ml", 1000, true},
		{500, "g", "kg", 0.5, true},
		{2, "kg", "g", 2000, true},
		{1, "tbsp", "ml", 15, true},
		{3, "pc", "pc", 3, true},         // same unit
		{5, "ml", "g", 5, false},         // cross-dimension → unchanged, not ok
		{5, "widget", "L", 5, false},     // unknown → unchanged, not ok
	}
	for _, c := range cases {
		got, ok := Convert(c.qty, c.from, c.to)
		if ok != c.ok {
			t.Errorf("Convert(%v,%q,%q) ok=%v want %v", c.qty, c.from, c.to, ok, c.ok)
		}
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("Convert(%v,%q,%q)=%v want %v", c.qty, c.from, c.to, got, c.want)
		}
	}
}
