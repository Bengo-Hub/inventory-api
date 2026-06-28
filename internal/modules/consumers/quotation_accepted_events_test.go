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

// TestParseQuotationAcceptedOutletID verifies the top-level outlet_id (branch) on the event
// payload is decoded so the consumer can resolve a branch-aware receiving warehouse.
func TestParseQuotationAcceptedOutletID(t *testing.T) {
	tenantID := uuid.New()
	outletID := uuid.New()
	payload := map[string]any{
		"quotation_id":     uuid.New().String(),
		"quotation_number": "QT-2026-0099",
		"currency":         "KES",
		"outlet_id":        outletID.String(),
		"lines":            []map[string]any{},
	}
	evt := eventslib.NewEvent("quotation_accepted", "treasury", uuid.New(), tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var envelope struct {
		Payload quotationAcceptedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.Payload.OutletID != outletID.String() {
		t.Errorf("outlet_id = %q, want %q", envelope.Payload.OutletID, outletID.String())
	}

	// Absent outlet_id (tenant-wide quotation) decodes to empty string -> default-warehouse path.
	payload2 := map[string]any{
		"quotation_id":     uuid.New().String(),
		"quotation_number": "QT-2026-0100",
		"currency":         "KES",
		"lines":            []map[string]any{},
	}
	evt2 := eventslib.NewEvent("quotation_accepted", "treasury", uuid.New(), tenantID, payload2)
	data2, _ := evt2.ToJSON()
	var envelope2 struct {
		Payload quotationAcceptedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data2, &envelope2); err != nil {
		t.Fatalf("unmarshal envelope2: %v", err)
	}
	if envelope2.Payload.OutletID != "" {
		t.Errorf("absent outlet_id should decode to empty, got %q", envelope2.Payload.OutletID)
	}
}

// TestGroupBySupplier verifies per-vendor PO splitting: lines are grouped by each item's
// preferred_supplier_id, items with no preferred supplier collect into the uuid.Nil bucket,
// and supplier keys come back in stable first-seen order.
func TestGroupBySupplier(t *testing.T) {
	supA := uuid.New()
	supB := uuid.New()

	itemA1 := &ent.Item{ID: uuid.New(), PreferredSupplierID: &supA}
	itemB1 := &ent.Item{ID: uuid.New(), PreferredSupplierID: &supB}
	itemA2 := &ent.Item{ID: uuid.New(), PreferredSupplierID: &supA}
	itemNone := &ent.Item{ID: uuid.New()} // no preferred supplier

	resolved := []resolvedLine{
		{item: itemA1, quantity: 2, unitCost: 10},
		{item: itemB1, quantity: 1, unitCost: 50},
		{item: itemNone, quantity: 5, unitCost: 3},
		{item: itemA2, quantity: 4, unitCost: 7},
	}

	groups, order := groupBySupplier(resolved)

	// Three buckets: supA, supB, uuid.Nil (supplier-less).
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	// Stable first-seen order: supA, supB, Nil.
	wantOrder := []uuid.UUID{supA, supB, uuid.Nil}
	if len(order) != len(wantOrder) {
		t.Fatalf("order len = %d, want %d", len(order), len(wantOrder))
	}
	for i, k := range wantOrder {
		if order[i] != k {
			t.Errorf("order[%d] = %s, want %s", i, order[i], k)
		}
	}
	// supA has two lines (itemA1, itemA2).
	if got := len(groups[supA]); got != 2 {
		t.Errorf("supA lines = %d, want 2", got)
	}
	// supB has one line.
	if got := len(groups[supB]); got != 1 {
		t.Errorf("supB lines = %d, want 1", got)
	}
	// supplier-less bucket (uuid.Nil) has one line.
	if got := len(groups[uuid.Nil]); got != 1 {
		t.Errorf("supplier-less lines = %d, want 1", got)
	}
	// Line content carries item id + qty + cost through.
	if groups[supB][0].itemID != itemB1.ID || groups[supB][0].quantity != 1 || groups[supB][0].unitCost != 50 {
		t.Errorf("supB line mismatch: %+v", groups[supB][0])
	}
}

// TestGroupBySupplierAllUnassigned verifies that when no item has a preferred supplier, all lines
// collect into the single supplier-less bucket (current/legacy behavior).
func TestGroupBySupplierAllUnassigned(t *testing.T) {
	resolved := []resolvedLine{
		{item: &ent.Item{ID: uuid.New()}, quantity: 1, unitCost: 10},
		{item: &ent.Item{ID: uuid.New()}, quantity: 2, unitCost: 20},
	}
	groups, order := groupBySupplier(resolved)
	if len(groups) != 1 || len(order) != 1 || order[0] != uuid.Nil {
		t.Fatalf("want single uuid.Nil bucket, got groups=%d order=%v", len(groups), order)
	}
	if got := len(groups[uuid.Nil]); got != 2 {
		t.Errorf("supplier-less lines = %d, want 2", got)
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
