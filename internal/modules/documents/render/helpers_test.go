package render

import "testing"

func TestVarianceQtyText(t *testing.T) {
	cases := []struct {
		name, qty, received, want string
	}{
		{"full match", "10", "10", "0"},
		{"shortfall", "10", "8", "-2"},
		{"fractional shortfall", "5.5", "5", "-0.5"},
		{"missing received", "10", "", ""},
		{"unparseable", "10", "n/a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := varianceQtyText(c.qty, c.received); got != c.want {
				t.Fatalf("varianceQtyText(%q, %q) = %q, want %q", c.qty, c.received, got, c.want)
			}
		})
	}
}
