package stock

import "testing"

// TestCapReverseQty_MultiRowSameKey_SharesSharedBudget is the regression test for a systematic
// under-return of stock: a SKU consumed across TWO ConsumptionLine rows sharing the same
// recipeSKU|ingredientSKU key (a repeated sale line, or an Edit-Sale increase that recorded a
// second consumption row for a SKU already on the order) must share ONE reversal budget — the
// sum of both rows' quantities — not have each row capped against only its own Quantity.
// Worked example from the live bug: line A qty=5, line B qty=2 (same key), reversing 2 of a
// 7-total sold quantity (ratio 2/7) must return exactly 2 total (1.4286 + 0.5714), not silently
// drop line B's share.
func TestCapReverseQty_MultiRowSameKey_SharesSharedBudget(t *testing.T) {
	const keyTotalQty = 5 + 2 // both lines share this key
	ratio := 2.0 / 7.0
	reversedSoFar := map[string]float64{}
	const key = "recipe-1|ingredient-1"

	gotA := capReverseQty(5, ratio, keyTotalQty, reversedSoFar[key])
	reversedSoFar[key] += gotA
	if want := 1.4286; gotA != want {
		t.Errorf("line A reverseQty = %v, want %v", gotA, want)
	}

	gotB := capReverseQty(2, ratio, keyTotalQty, reversedSoFar[key])
	reversedSoFar[key] += gotB
	if want := 0.5714; gotB != want {
		t.Errorf("line B reverseQty = %v, want %v (this is exactly what the old per-row cap silently dropped to 0)", gotB, want)
	}

	if total := reversedSoFar[key]; total != 2.0 {
		t.Errorf("total reversed across both rows = %v, want 2.0 (the full requested reversal)", total)
	}
}

// TestCapReverseQty_SingleRow_UnchangedBehavior covers the common case (one row per key) —
// behavior must be byte-identical to before this fix.
func TestCapReverseQty_SingleRow_UnchangedBehavior(t *testing.T) {
	got := capReverseQty(10, 0.5, 10, 0)
	if want := 5.0; got != want {
		t.Errorf("reverseQty = %v, want %v", got, want)
	}
}

// TestCapReverseQty_PriorCallAlreadyReversedSomeOfTheKey ensures a SECOND ReverseConsumption
// call (e.g. a follow-up partial reduction) correctly sees what an EARLIER call already
// consumed from the shared key's budget.
func TestCapReverseQty_PriorCallAlreadyReversedSomeOfTheKey(t *testing.T) {
	// Prior call already reversed 6 of a 10-total key budget; this call asks for the full
	// remaining line quantity (ratio 1) but only 4 is left.
	got := capReverseQty(10, 1, 10, 6)
	if want := 4.0; got != want {
		t.Errorf("reverseQty = %v, want %v (capped to what's left of the shared budget)", got, want)
	}
}

func TestCapReverseQty_ZeroRatioOrQuantity_ReturnsZero(t *testing.T) {
	if got := capReverseQty(10, 0, 10, 0); got != 0 {
		t.Errorf("ratio=0: reverseQty = %v, want 0", got)
	}
	if got := capReverseQty(0, 1, 10, 0); got != 0 {
		t.Errorf("lineQty=0: reverseQty = %v, want 0", got)
	}
}

func TestCapReverseQty_BudgetExhausted_ReturnsZero(t *testing.T) {
	// The whole 10-unit key budget was already reversed by prior rows/calls.
	got := capReverseQty(5, 1, 10, 10)
	if got != 0 {
		t.Errorf("reverseQty = %v, want 0 (nothing left of the shared budget)", got)
	}
}

// TestApportionDeducted_MultiLineGroup_ShortfallExceedsFirstLine is the regression test for
// the multi-lot/layer-fallback over-return bug: a sale's deduction for one SKU split across two
// ConsumptionLine rows (e.g. two cost layers drawn from), with a shortfall LARGER than the
// first line's own quantity. The old (sku, exact-quantity) matching heuristic attributed the
// entire shortfall to whichever line matched first and returned 0 for the other, over-crediting
// the group's total deducted amount. Worked example: line A qty=1, line B qty=4 (group total 5),
// header shortfall=2 → true group-deducted total is 3, apportioned 0.6/2.4 by each line's share.
func TestApportionDeducted_MultiLineGroup_ShortfallExceedsFirstLine(t *testing.T) {
	const groupTotalQty = 1 + 4
	const groupShortfall = 2.0

	gotA := apportionDeducted(1, groupTotalQty, groupShortfall)
	if want := 0.6; gotA != want {
		t.Errorf("line A deducted = %v, want %v", gotA, want)
	}
	gotB := apportionDeducted(4, groupTotalQty, groupShortfall)
	if want := 2.4; gotB != want {
		t.Errorf("line B deducted = %v, want %v", gotB, want)
	}
	if total := gotA + gotB; total != 3.0 {
		t.Errorf("total deducted across both lines = %v, want 3.0 (5 needed - 2 shortfall); the old heuristic over-returned this to 4.0", total)
	}
}

// TestApportionDeducted_SingleLine_NoShortfall covers the common case: one line, fully covered.
func TestApportionDeducted_SingleLine_NoShortfall(t *testing.T) {
	if got, want := apportionDeducted(5, 5, 0), 5.0; got != want {
		t.Errorf("apportionDeducted(5, 5, 0) = %v, want %v", got, want)
	}
}

// TestApportionDeducted_ZeroGroupTotal_FallsBackToLineQty guards the (theoretically unreachable
// in practice, since callers skip zero/theoretical lines before calling) division-by-zero edge.
func TestApportionDeducted_ZeroGroupTotal_FallsBackToLineQty(t *testing.T) {
	if got, want := apportionDeducted(3, 0, 0), 3.0; got != want {
		t.Errorf("apportionDeducted(3, 0, 0) = %v, want %v", got, want)
	}
}
