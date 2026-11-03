package stock

import (
	"testing"
	"time"
)

func TestReversalKind(t *testing.T) {
	cases := []struct {
		kind, key, reason, want string
	}{
		{"edit", "pos-shred-1", "", ReversalKindEdit},                                      // explicit kind wins
		{"", "pos-shred-1", "Delete Sale (shred): wrong customer", ReversalKindVoid},       // BOI 005924
		{"", "pos-return-1", "Customer return 00012", ReversalKindReturn},                  // return flow
		{"", "pos-exchange-1", "", ReversalKindReturn},                                     // exchange
		{"", "pos-reversal-1", "Reversal 00031: Delete Sale: duplicate", ReversalKindVoid}, // fiscalised delete
		{"", "pos-reversal-1", "Reversal 00032: Edit Sale: qty typo", ReversalKindEdit},    // edit decrease
		{"", "pos-reversal-1", "Reversal 00033: refund agreed", ReversalKindReversal},      // tool reversal
		{"bogus", "", "", ReversalKindReversal},                                            // unknown kind ignored
	}
	for _, c := range cases {
		if got := ReversalKind(c.kind, c.key, c.reason); got != c.want {
			t.Errorf("ReversalKind(%q,%q,%q) = %q, want %q", c.kind, c.key, c.reason, got, c.want)
		}
	}
}

func TestReversalRowType(t *testing.T) {
	if ty, l := reversalRowType(ReversalKindVoid); ty != "sale_reversal" || l != "Sale Voided" {
		t.Errorf("void = %s/%s", ty, l)
	}
	if ty, l := reversalRowType(ReversalKindReturn); ty != "sell_return" || l != "Customer Return" {
		t.Errorf("return = %s/%s", ty, l)
	}
}

// The reported BOI case: SOLD 3 (005924), SOLD 6 (005925), then 005924 deleted. The summary must
// read Total Sold 6 with 3 voided, not Sold 9 plus a 3 unit sell return.
func TestApplyToSummaryNetsVoids(t *testing.T) {
	var s StockHistorySummary
	applyToSummary(&s, "sale", -3)
	applyToSummary(&s, "sale", -6)
	applyToSummary(&s, "sale_reversal", 3)
	if s.TotalSold != 6 || s.TotalSaleReversals != 3 || s.TotalSellReturns != 0 {
		t.Errorf("summary = sold %v, reversals %v, sell returns %v; want 6, 3, 0", s.TotalSold, s.TotalSaleReversals, s.TotalSellReturns)
	}
}

func TestAllocateSaleCorrections(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	sales := []saleRef{
		{idx: 0, key: "A|w", qty: 3, at: t0},                  // fully voided
		{idx: 1, key: "B|w", qty: 6, at: t0},                  // untouched
		{idx: 2, key: "C|w", qty: 5, at: t0},                  // 2 returned
		{idx: 3, key: "D|w", qty: 2, at: t0},                  // first row of D
		{idx: 4, key: "D|w", qty: 2, at: t0.Add(time.Minute)}, // edit increase row of D
	}
	reversed := map[string]map[string]float64{
		"A|w": {ReversalKindVoid: 3},
		"C|w": {ReversalKindReturn: 2},
		"D|w": {ReversalKindVoid: 3},
	}
	got := allocateSaleCorrections(sales, reversed)
	want := map[int]saleCorrection{
		0: {Quantity: 3, Status: "voided"},
		2: {Quantity: 2, Status: "partially_returned"},
		3: {Quantity: 2, Status: "voided"},
		4: {Quantity: 1, Status: "partially_voided"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for idx, w := range want {
		if got[idx] != w {
			t.Errorf("row %d = %+v, want %+v", idx, got[idx], w)
		}
	}
}

func TestDisplayLabel(t *testing.T) {
	if l := (MovementRow{Label: "Sold", Status: "voided", ReversedQuantity: 3}).DisplayLabel(); l != "Sold (Voided)" {
		t.Errorf("voided label = %q", l)
	}
	if l := (MovementRow{Label: "Sold", Status: "partially_returned", ReversedQuantity: 2}).DisplayLabel(); l != "Sold (Partly returned 2)" {
		t.Errorf("partial label = %q", l)
	}
	if l := (MovementRow{Label: "Sold"}).DisplayLabel(); l != "Sold" {
		t.Errorf("plain label = %q", l)
	}
}
