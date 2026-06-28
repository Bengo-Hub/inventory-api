package consumers

import (
	"encoding/json"
	"testing"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

// TestParseQuotationAcceptedEnvelope verifies the consumer parses the exact shared-events
// envelope treasury-api emits (tenant_id at the top level; quotation fields + decimal-string
// quantities inside payload).
func TestParseQuotationAcceptedEnvelope(t *testing.T) {
	tenantID := uuid.New()
	quotationID := uuid.New()
	itemID := uuid.New()

	// Mirror treasury invoicing.OutboxEventPublisher: NewEvent(eventType, aggregateType, ...)
	// with the quotation payload (decimal quantities marshal to JSON strings).
	payload := map[string]any{
		"quotation_id":     quotationID.String(),
		"quotation_number": "QT-2026-0007",
		"customer_name":    "Acme Ltd",
		"crm_customer_id":  "crm-123",
		"currency":         "KES",
		"lines": []map[string]any{
			{"item_id": itemID.String(), "sku": "SKU-1", "description": "Widget", "quantity": "3", "unit_price": "150.00"},
			{"item_id": "", "sku": "SKU-2", "description": "Gadget", "quantity": 2.5, "unit_price": 99.0},
		},
	}
	evt := eventslib.NewEvent("quotation_accepted", "treasury", uuid.New(), tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var envelope struct {
		TenantID string                   `json:"tenant_id"`
		Payload  quotationAcceptedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if envelope.TenantID != tenantID.String() {
		t.Errorf("tenant_id = %q, want %q", envelope.TenantID, tenantID.String())
	}
	if envelope.Payload.QuotationID != quotationID.String() {
		t.Errorf("quotation_id = %q, want %q", envelope.Payload.QuotationID, quotationID.String())
	}
	if envelope.Payload.QuotationNumber != "QT-2026-0007" {
		t.Errorf("quotation_number = %q", envelope.Payload.QuotationNumber)
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
	if envelope.Payload.Lines[1].ItemID != "" {
		t.Errorf("line[1].item_id should be empty, got %q", envelope.Payload.Lines[1].ItemID)
	}
}

func f64p(v float64) *float64 { return &v }

func TestBuyingCost(t *testing.T) {
	tests := []struct {
		name string
		item *ent.Item
		want float64
	}{
		{"explicit cost_price", &ent.Item{CostPrice: f64p(42)}, 42},
		{"derived from purchase fields", &ent.Item{PurchasePrice: f64p(750), PurchasePackSize: f64p(1000), YieldPct: f64p(0.5)}, 750.0 / 1000.0 / 0.5},
		{"derived default yield", &ent.Item{PurchasePrice: f64p(100), PurchasePackSize: f64p(10)}, 10},
		{"no cost data -> 0", &ent.Item{}, 0},
		{"zero cost_price falls through to 0", &ent.Item{CostPrice: f64p(0)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buyingCost(tt.item); got != tt.want {
				t.Errorf("buyingCost() = %v, want %v", got, tt.want)
			}
		})
	}
}
