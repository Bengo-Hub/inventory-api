package stock

import (
	"encoding/json"
	"testing"
)

// TestReservationItemModifierContract pins the JSON wire format of the modifier-carrying
// reservation/consumption DTOs (STK-4). ordering-backend and the pos.sale.finalized consumer
// both depend on these exact field names (`modifiers`, `inventory_modifier_option_id`, `sku`,
// `quantity`) to deduct variant + modifier stock, so a tag drift would silently stop modifier
// stock from being reserved/consumed.
func TestReservationItemModifierContract(t *testing.T) {
	raw := `{
		"sku": "BURGER",
		"quantity": 2,
		"modifiers": [
			{"inventory_modifier_option_id": "11111111-1111-1111-1111-111111111111", "quantity": 1},
			{"sku": "CHEESE-SLICE", "quantity": 2}
		]
	}`

	var ri ReservationItem
	if err := json.Unmarshal([]byte(raw), &ri); err != nil {
		t.Fatalf("unmarshal ReservationItem: %v", err)
	}
	if ri.SKU != "BURGER" || ri.Quantity != 2 {
		t.Fatalf("base fields wrong: %+v", ri)
	}
	if len(ri.Modifiers) != 2 {
		t.Fatalf("expected 2 modifiers, got %d", len(ri.Modifiers))
	}
	if ri.Modifiers[0].InventoryModifierOptionID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("modifier[0] option id not decoded: %+v", ri.Modifiers[0])
	}
	if ri.Modifiers[1].SKU != "CHEESE-SLICE" || ri.Modifiers[1].Quantity != 2 {
		t.Errorf("modifier[1] (direct sku) not decoded: %+v", ri.Modifiers[1])
	}

	// ConsumptionItem shares the same ModifierLine contract.
	var ci ConsumptionItem
	if err := json.Unmarshal([]byte(raw), &ci); err != nil {
		t.Fatalf("unmarshal ConsumptionItem: %v", err)
	}
	if len(ci.Modifiers) != 2 {
		t.Fatalf("ConsumptionItem expected 2 modifiers, got %d", len(ci.Modifiers))
	}

	// Round-trip: a line with no modifiers must omit the field entirely (omitempty), so the
	// existing no-modifier callers' payloads are byte-for-byte unchanged.
	out, err := json.Marshal(ReservationItem{SKU: "X", Quantity: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(out); got != `{"sku":"X","quantity":1}` {
		t.Errorf("no-modifier reservation item changed wire format: %s", got)
	}
}
