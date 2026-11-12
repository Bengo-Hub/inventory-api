package stock

import "testing"

func TestIsRecipeComponent(t *testing.T) {
	cases := []struct {
		sku, finished string
		want          bool
	}{
		{"ING-SUGAR", "BEV-HOT-001", true},  // sugar used by a hot chocolate
		{"RTL-BSP-001", "RTL-BSP-001", false}, // body wash sold as itself
		{"rtl-bsp-001", "RTL-BSP-001", false}, // same item, different case
		{"ING-SUGAR", "", false},              // no finished item recorded: not reported
	}
	for _, c := range cases {
		if got := isRecipeComponent(c.sku, c.finished); got != c.want {
			t.Errorf("isRecipeComponent(%q, %q) = %v, want %v", c.sku, c.finished, got, c.want)
		}
	}
}
