package stock

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

// TestEntryUnitNoOpWhenSameUnit pins that an adjustment in the stock unit (or with no unit)
// is never touched and needs no database lookup.
func TestEntryUnitNoOpWhenSameUnit(t *testing.T) {
	uid := uuid.New()
	itm := &ent.Item{Sku: "X", UnitID: &uid}
	itm.Edges.Units = &ent.Unit{Abbreviation: "g", Name: "GRAM"}

	req := AdjustStockRequest{Adjustment: 5, UnitID: &uid}
	if err := convertAdjustmentToStockUnit(nil, nil, itm, &req); err != nil || req.Adjustment != 5 || req.Notes != "" {
		t.Fatalf("same unit: got (%v, %q, %v), want (5, \"\", nil)", req.Adjustment, req.Notes, err)
	}
	req = AdjustStockRequest{Adjustment: 7}
	if err := convertAdjustmentToStockUnit(nil, nil, itm, &req); err != nil || req.Adjustment != 7 {
		t.Fatalf("no unit: got (%v, %v), want (7, nil)", req.Adjustment, err)
	}
}

func TestEntryUnitErrorMessage(t *testing.T) {
	e := &EntryUnitError{SKU: "INGR-LIME", From: "pc", To: "kg"}
	if e.Error() == "" {
		t.Fatal("empty message")
	}
}
