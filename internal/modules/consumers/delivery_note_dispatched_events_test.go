package consumers

import (
	"encoding/json"
	"strings"
	"testing"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
)

// TestParseDeliveryNoteDispatchedEnvelope verifies the consumer parses the exact shared-events
// envelope treasury-api emits for treasury.delivery_note.dispatched: tenant_id at the top level;
// delivery-note fields + decimal-string quantities inside payload.
func TestParseDeliveryNoteDispatchedEnvelope(t *testing.T) {
	tenantID := uuid.New()
	deliveryNoteID := uuid.New()
	itemID := uuid.New()
	outletID := uuid.New()

	payload := map[string]any{
		"delivery_note_id":     deliveryNoteID.String(),
		"delivery_note_number": "DN-2026-0042",
		"source_invoice_id":    uuid.New().String(),
		"outlet_id":            outletID.String(),
		"customer_name":        "Acme Ltd",
		"lines": []map[string]any{
			// item_id as a UUID; quantity as a decimal string (decimal.Decimal marshals to string)
			{"item_id": itemID.String(), "sku": "SKU-1", "description": "Widget", "quantity": "3"},
			// item_id as a SKU string (sku-or-uuid); quantity as a JSON number
			{"item_id": "SKU-2", "sku": "SKU-2", "description": "Gadget", "quantity": 2.5},
		},
	}
	evt := eventslib.NewEvent("delivery_note.dispatched", "treasury", uuid.New(), tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var envelope struct {
		TenantID string                        `json:"tenant_id"`
		Payload  deliveryNoteDispatchedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if envelope.TenantID != tenantID.String() {
		t.Errorf("tenant_id = %q, want %q", envelope.TenantID, tenantID.String())
	}
	if envelope.Payload.DeliveryNoteID != deliveryNoteID.String() {
		t.Errorf("delivery_note_id = %q, want %q", envelope.Payload.DeliveryNoteID, deliveryNoteID.String())
	}
	if envelope.Payload.DeliveryNoteNumber != "DN-2026-0042" {
		t.Errorf("delivery_note_number = %q", envelope.Payload.DeliveryNoteNumber)
	}
	if envelope.Payload.OutletID != outletID.String() {
		t.Errorf("outlet_id = %q, want %q", envelope.Payload.OutletID, outletID.String())
	}
	if len(envelope.Payload.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(envelope.Payload.Lines))
	}
	// Decimal-as-string quantity must decode to a number.
	if got := float64(envelope.Payload.Lines[0].Quantity); got != 3 {
		t.Errorf("line[0].quantity = %v, want 3", got)
	}
	if envelope.Payload.Lines[0].ItemID != itemID.String() {
		t.Errorf("line[0].item_id = %q, want %q", envelope.Payload.Lines[0].ItemID, itemID.String())
	}
	// Numeric quantity must also decode.
	if got := float64(envelope.Payload.Lines[1].Quantity); got != 2.5 {
		t.Errorf("line[1].quantity = %v, want 2.5", got)
	}
	// item_id may be a sku-or-uuid string — here it is a SKU.
	if envelope.Payload.Lines[1].ItemID != "SKU-2" {
		t.Errorf("line[1].item_id = %q, want SKU-2", envelope.Payload.Lines[1].ItemID)
	}
}

// TestParseDeliveryNoteDispatchedTenantWide verifies a tenant-wide delivery note (empty outlet_id)
// decodes to an empty string so the consumer falls back to the default warehouse.
func TestParseDeliveryNoteDispatchedTenantWide(t *testing.T) {
	tenantID := uuid.New()
	payload := map[string]any{
		"delivery_note_id":     uuid.New().String(),
		"delivery_note_number": "DN-2026-0099",
		"outlet_id":            "",
		"lines":                []map[string]any{},
	}
	evt := eventslib.NewEvent("delivery_note.dispatched", "treasury", uuid.New(), tenantID, payload)
	data, _ := evt.ToJSON()

	var envelope struct {
		Payload deliveryNoteDispatchedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.Payload.OutletID != "" {
		t.Errorf("tenant-wide outlet_id should decode to empty, got %q", envelope.Payload.OutletID)
	}
}

// TestGoodsIssueReference verifies the reference/idempotency key is a stable, delivery-note-scoped
// string so the same dispatch maps to the same key across JetStream redeliveries, and distinct
// dispatches map to distinct keys.
func TestGoodsIssueReference(t *testing.T) {
	dn1 := uuid.New()
	dn2 := uuid.New()

	ref1 := goodsIssueReference(dn1)
	if ref1 != goodsIssueReference(dn1) {
		t.Errorf("reference not stable for the same delivery note: %q vs %q", ref1, goodsIssueReference(dn1))
	}
	if !strings.Contains(ref1, dn1.String()) {
		t.Errorf("reference %q should contain delivery_note_id %q", ref1, dn1.String())
	}
	if ref1 == goodsIssueReference(dn2) {
		t.Errorf("distinct delivery notes must yield distinct references, both = %q", ref1)
	}
}
