package stock

import (
	"testing"
	"time"
)

// classifyAdjustment maps every StockAdjustment reason into the unified
// movement taxonomy — the summary cards depend on this mapping being stable.
func TestClassifyAdjustment(t *testing.T) {
	cases := []struct {
		reason string
		change float64
		want   string
	}{
		{"opening_balance", 10, "opening_stock"},
		{"initial_count", 5, "opening_stock"},
		{"transfer_in", 3, "transfer_in"},
		{"transfer_out", -3, "transfer_out"},
		{"return", 2, "sell_return"},        // positive: customer return back in
		{"return", -2, "purchase_return"},   // negative: back to supplier
		{"return", 0, "sell_return"},        // zero treated as inbound (no stock effect)
		{"damaged", -1, "adjustment"},
		{"expired", -4, "adjustment"},
		{"shrinkage", -2, "adjustment"},
		{"found", 1, "adjustment"},
		{"correction", 2, "adjustment"},
		{"count_variance", -1, "adjustment"},
		{"internal_consumption", -6, "adjustment"},
		{"location_hidden", 0, "adjustment"},
		{"location_unhidden", 0, "adjustment"},
		{"other", 9, "adjustment"},
	}
	for _, c := range cases {
		got, label := classifyAdjustment(c.reason, c.change)
		if got != c.want {
			t.Errorf("classifyAdjustment(%q,%v) = %q, want %q", c.reason, c.change, got, c.want)
		}
		if label == "" {
			t.Errorf("classifyAdjustment(%q,%v) returned empty label", c.reason, c.change)
		}
	}
}

// The quantities-in/out cards must aggregate with the Go-Digital sign
// conventions: IN figures positive, OUT figures reported positive too
// (their movements carry negative change).
func TestApplyToSummary(t *testing.T) {
	var s StockHistorySummary
	applyToSummary(&s, "opening_stock", 270)
	applyToSummary(&s, "purchase", 50)
	applyToSummary(&s, "sale", -30)         // stored negative
	applyToSummary(&s, "sale", -20)
	applyToSummary(&s, "sell_return", 5)
	applyToSummary(&s, "purchase_return", -8) // stored negative
	applyToSummary(&s, "transfer_in", 12)
	applyToSummary(&s, "transfer_out", -7) // stored negative
	applyToSummary(&s, "adjustment", -3)
	applyToSummary(&s, "adjustment", 1)

	if s.OpeningStock != 270 {
		t.Errorf("OpeningStock = %v, want 270", s.OpeningStock)
	}
	if s.TotalPurchased != 50 {
		t.Errorf("TotalPurchased = %v, want 50", s.TotalPurchased)
	}
	if s.TotalSold != 50 {
		t.Errorf("TotalSold = %v, want 50 (reported positive)", s.TotalSold)
	}
	if s.TotalSellReturns != 5 {
		t.Errorf("TotalSellReturns = %v, want 5", s.TotalSellReturns)
	}
	if s.TotalPurchaseReturns != 8 {
		t.Errorf("TotalPurchaseReturns = %v, want 8 (reported positive)", s.TotalPurchaseReturns)
	}
	if s.TransfersIn != 12 {
		t.Errorf("TransfersIn = %v, want 12", s.TransfersIn)
	}
	if s.TransfersOut != 7 {
		t.Errorf("TransfersOut = %v, want 7 (reported positive)", s.TransfersOut)
	}
	if s.TotalAdjusted != -2 {
		t.Errorf("TotalAdjusted = %v, want -2 (net)", s.TotalAdjusted)
	}
	// Unknown type must be a no-op, not a panic or misfile.
	before := s
	applyToSummary(&s, "mystery", 99)
	if s != before {
		t.Errorf("unknown movement type mutated the summary: %+v", s)
	}
}

// Date-window filter: inclusive bounds behaviour.
func TestStockHistoryFilterInRange(t *testing.T) {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	f := StockHistoryFilter{DateFrom: &from, DateTo: &to}

	if f.inRange(from.Add(-time.Second)) {
		t.Error("before DateFrom must be excluded")
	}
	if !f.inRange(from) {
		t.Error("DateFrom itself must be included")
	}
	if !f.inRange(to) {
		t.Error("DateTo itself must be included")
	}
	if f.inRange(to.Add(time.Second)) {
		t.Error("after DateTo must be excluded")
	}
	// Open-ended filter accepts everything.
	open := StockHistoryFilter{}
	if !open.inRange(time.Now()) || !open.inRange(time.Time{}) {
		t.Error("open filter must accept any time")
	}
}
