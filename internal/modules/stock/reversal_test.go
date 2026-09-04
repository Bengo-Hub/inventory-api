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

// TestCapReverseQty_FullReversal_ReturnsFullOriginalQuantity pins the round-trip invariant a live
// test against the deployed fix caught a real regression in: RecordConsumption/ConsumeReservation
// decrement on_hand by the FULL requested quantity up front (letting the balance go negative
// rather than flooring at whatever portion real stock/cost-layers could cover — see
// [[oversell-negative-stock-settlement]]), so a full reversal (ratio 1, nothing reversed yet) must
// give back that same full quantity for ReverseConsumption's stockReturn to be symmetric with the
// forward decrement — NOT just the "physically covered" portion net of any shortfall, which was
// this file's earlier (wrong, since reverted) `apportionDeducted` approach. Live-verified:
// baseline on_hand=5, oversell qty=8 -> on_hand=-3 (shortfall=3); a full void must restore on_hand
// to exactly 5, meaning stockReturn must be 8, not 5. capReverseQty(8, ratio=1, keyTotalQty=8,
// reversedSoFar=0) is exactly what ReverseConsumption uses as reverseQty/stockReturn for a
// non-theoretical line reversed in full.
func TestCapReverseQty_FullReversal_ReturnsFullOriginalQuantity(t *testing.T) {
	if got, want := capReverseQty(8, 1, 8, 0), 8.0; got != want {
		t.Errorf("capReverseQty(8, 1, 8, 0) = %v, want %v (must return the FULL original quantity, including the shortfall portion, for on_hand to round-trip back to its pre-oversell value)", got, want)
	}
}
