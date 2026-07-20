package handlers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// jsonRoundTrip mirrors what persistence does to the payload: it is marshalled to
// jsonb on write and decoded back into a map[string]any on read, so numbers come
// back as float64 and UUIDs as strings. Parsing must survive that trip.
func jsonRoundTrip(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

// TestAdjustmentPayloadRoundTrip is the regression guard for the reported bug: an
// approved adjustment must post the SAME movement that was submitted. If the payload
// build/parse boundary drops or mistypes any field, the approval-execution path would
// post nothing (or the wrong quantity) — the exact "stock never changes" symptom.
func TestAdjustmentPayloadRoundTrip(t *testing.T) {
	actor := uuid.New()
	wh := uuid.New()
	outlet := uuid.New()
	unit := uuid.New()

	orig := stock.AdjustStockRequest{
		SKU:         "RAW-ING-013",
		Adjustment:  42,
		Reason:      "correction",
		Reference:   "count-2026",
		Notes:       "physical recount",
		WarehouseID: wh,
		OutletID:    outlet,
		UnitID:      &unit,
	}

	payload := jsonRoundTrip(t, adjustmentPayload(orig, actor))
	got, ok := adjustmentRequestFromPayload(payload)
	if !ok {
		t.Fatal("expected payload to be replayable")
	}
	if got.SKU != orig.SKU {
		t.Errorf("sku: got %q want %q", got.SKU, orig.SKU)
	}
	if got.Adjustment != orig.Adjustment {
		t.Errorf("adjustment: got %v want %v", got.Adjustment, orig.Adjustment)
	}
	if got.Reason != orig.Reason {
		t.Errorf("reason: got %q want %q", got.Reason, orig.Reason)
	}
	if got.Reference != orig.Reference {
		t.Errorf("reference: got %q want %q", got.Reference, orig.Reference)
	}
	if got.Notes != orig.Notes {
		t.Errorf("notes: got %q want %q", got.Notes, orig.Notes)
	}
	if got.AdjustedBy != actor {
		t.Errorf("adjusted_by: got %v want %v", got.AdjustedBy, actor)
	}
	if got.WarehouseID != wh {
		t.Errorf("warehouse_id: got %v want %v", got.WarehouseID, wh)
	}
	if got.OutletID != outlet {
		t.Errorf("outlet_id: got %v want %v", got.OutletID, outlet)
	}
	if got.UnitID == nil || *got.UnitID != unit {
		t.Errorf("unit_id: got %v want %v", got.UnitID, unit)
	}
}

// TestAdjustmentPayloadNegativeAndMinimal covers a downward write-off carrying only the
// required fields (no warehouse/outlet/unit) — the optional-omitted branches must not
// fabricate zero UUIDs, and a negative delta must survive intact.
func TestAdjustmentPayloadNegativeAndMinimal(t *testing.T) {
	actor := uuid.New()
	orig := stock.AdjustStockRequest{SKU: "RAW-ING-013", Adjustment: -7, Reason: "damaged"}

	payload := jsonRoundTrip(t, adjustmentPayload(orig, actor))
	got, ok := adjustmentRequestFromPayload(payload)
	if !ok {
		t.Fatal("expected minimal payload to be replayable")
	}
	if got.Adjustment != -7 {
		t.Errorf("adjustment: got %v want -7", got.Adjustment)
	}
	if got.WarehouseID != uuid.Nil {
		t.Errorf("warehouse_id should stay nil, got %v", got.WarehouseID)
	}
	if got.OutletID != uuid.Nil {
		t.Errorf("outlet_id should stay nil, got %v", got.OutletID)
	}
	if got.UnitID != nil {
		t.Errorf("unit_id should stay nil, got %v", *got.UnitID)
	}
}

// TestAdjustmentPayloadUnreplayable ensures the execution path refuses to post when the
// payload is missing or lacks the minimum to move stock — better to log and skip than to
// post a zero/blank adjustment.
func TestAdjustmentPayloadUnreplayable(t *testing.T) {
	cases := map[string]map[string]any{
		"nil payload":     nil,
		"empty payload":   {},
		"missing sku":     {"adjustment": float64(5)},
		"zero adjustment": {"sku": "X", "adjustment": float64(0)},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := adjustmentRequestFromPayload(p); ok {
				t.Errorf("expected %s to be unreplayable", name)
			}
		})
	}
}
