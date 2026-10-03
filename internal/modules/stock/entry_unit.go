package stock

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/stockcalc"
)

// EntryUnitError is returned when a stock movement was entered in a unit that can't be
// converted to the item's stock unit (no same-dimension conversion and no content-per-unit
// bridge). Rejecting it is the point: adding the raw number would corrupt the balance.
type EntryUnitError struct {
	SKU, From, To string
}

func (e *EntryUnitError) Error() string {
	return fmt.Sprintf("%s is stocked in %s; a quantity in %s can't be converted (set its content per unit, or enter it in %s)", e.SKU, e.To, e.From, e.To)
}

// convertAdjustmentToStockUnit rewrites req.Adjustment from the unit it was entered in
// (req.UnitID) into the item's stock unit, and notes the original entry on the adjustment so the
// audit trail shows "entered 5 kg = 5000 g". A request with no unit, or in the stock unit, is
// left untouched. itm must have its Units edge loaded.
func convertAdjustmentToStockUnit(ctx context.Context, tx *ent.Tx, itm *ent.Item, req *AdjustStockRequest) error {
	if req.UnitID == nil || itm.UnitID == nil || *req.UnitID == *itm.UnitID || itm.Edges.Units == nil {
		return nil
	}
	entry, err := tx.Unit.Get(ctx, *req.UnitID)
	if err != nil {
		return fmt.Errorf("stock: load entry unit: %w", err)
	}
	converted, ok := stockcalc.ConvertToStockUnit(itm, req.Adjustment, entry.Abbreviation)
	if !ok {
		return &EntryUnitError{SKU: itm.Sku, From: entry.Abbreviation, To: itm.Edges.Units.Abbreviation}
	}
	note := fmt.Sprintf("entered %s %s = %s %s", fmtQty(req.Adjustment), entry.Abbreviation, fmtQty(converted), itm.Edges.Units.Abbreviation)
	if strings.TrimSpace(req.Notes) == "" {
		req.Notes = note
	} else {
		req.Notes = req.Notes + " (" + note + ")"
	}
	req.Adjustment = round4(converted)
	return nil
}

func fmtQty(v float64) string {
	return strconv.FormatFloat(round4(v), 'f', -1, 64)
}
