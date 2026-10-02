package stock

import (
	"testing"

	"github.com/google/uuid"
)

func TestMergeReturnLines(t *testing.T) {
	got := mergeReturnLines([]ReturnRestockLine{
		{SKU: "A", Quantity: 1, OfQuantity: 3},
		{SKU: " A ", Quantity: 2, OfQuantity: 3},
		{SKU: "", Quantity: 5},
		{SKU: "B", Quantity: 0},
		{SKU: "C", Quantity: 1.5},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 lines (A merged, blanks and zero dropped), got %d: %+v", len(got), got)
	}
	if got[0].SKU != "A" || got[0].Quantity != 3 || got[0].OfQuantity != 3 {
		t.Errorf("A merged wrong: %+v", got[0])
	}
	if got[1].SKU != "C" || got[1].Quantity != 1.5 {
		t.Errorf("C wrong: %+v", got[1])
	}
}

func TestReversedToLines_GroupsBySKUAndWarehouseAndDropsTheoretical(t *testing.T) {
	whA, whB := uuid.New(), uuid.New()
	got := reversedToLines([]ReversedIngredient{
		{IngredientSKU: "X", StockReturned: 1, WarehouseID: whA},
		{IngredientSKU: "X", StockReturned: 2, WarehouseID: whA},
		{IngredientSKU: "X", StockReturned: 1, WarehouseID: whB},
		{IngredientSKU: "Y", QuantityReversed: 4, StockReturned: 0, WarehouseID: whA},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 rows (X@A, X@B; theoretical Y dropped), got %+v", got)
	}
	if got[0].Quantity != 3 || got[0].WarehouseID != whA || got[0].Method != "reversal" {
		t.Errorf("X@A wrong: %+v", got[0])
	}
	if got[1].Quantity != 1 || got[1].WarehouseID != whB {
		t.Errorf("X@B wrong: %+v", got[1])
	}
}
