package items

import "testing"

// TestApplyRequestedSellingPrice pins that a create request carrying only `selling_price` (the
// treasury invoice "create item" modal, S2S callers) persists it as the stored price instead of
// dropping it, while explicit Retail/Wholesale prices always win.
func TestApplyRequestedSellingPrice(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	d := ItemDTO{Type: "SERVICE", CostPrice: f(20000), SellingPrice: f(25000)}
	applyRequestedSellingPrice(&d)
	if d.MaxSellingPrice == nil || *d.MaxSellingPrice != 25000 || d.MinSellingPrice == nil || *d.MinSellingPrice != 25000 {
		t.Fatalf("selling_price not persisted: max=%v min=%v", d.MaxSellingPrice, d.MinSellingPrice)
	}

	d = ItemDTO{SellingPrice: f(25000), MaxSellingPrice: f(30000)}
	applyRequestedSellingPrice(&d)
	if *d.MaxSellingPrice != 30000 || d.MinSellingPrice != nil {
		t.Fatalf("explicit max must win and min stay unset: max=%v min=%v", *d.MaxSellingPrice, d.MinSellingPrice)
	}

	d = ItemDTO{SellingPrice: f(25000), MinSellingPrice: f(20000)}
	applyRequestedSellingPrice(&d)
	if *d.MaxSellingPrice != 25000 || *d.MinSellingPrice != 20000 {
		t.Fatalf("explicit min must be kept: max=%v min=%v", *d.MaxSellingPrice, *d.MinSellingPrice)
	}

	d = ItemDTO{CostPrice: f(100)}
	applyRequestedSellingPrice(&d)
	if d.MaxSellingPrice != nil || d.MinSellingPrice != nil {
		t.Fatal("no selling_price must leave the guardrails untouched")
	}
}
