package consumers

import (
	"encoding/json"
	"testing"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

// TestParseGoodsCommittedEnvelope decodes the exact envelope treasury emits: tenant at the top
// level, decimal quantities and costs as JSON strings inside the payload.
func TestParseGoodsCommittedEnvelope(t *testing.T) {
	tenantID, rootID, itemID, outletID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload := map[string]any{
		"root_id": rootID.String(), "root_number": "QT-260817-000012", "source_type": "quotation",
		"invoice_id": uuid.NewString(), "customer_name": "Flavia Home", "currency": "KES",
		"outlet_id": outletID.String(),
		"lines": []map[string]any{
			{"item_id": itemID.String(), "sku": "DVR-8", "description": "DVR", "quantity": "2", "unit_cost": "9000.00"},
			{"item_id": "", "sku": "", "description": "Cable", "quantity": 30.5, "unit_cost": 45},
		},
	}
	data, err := eventslib.NewEvent("goods_committed", "treasury", uuid.New(), tenantID, payload).ToJSON()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var env struct {
		TenantID string                `json:"tenant_id"`
		Payload  goodsCommittedPayload `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.TenantID != tenantID.String() || env.Payload.RootID != rootID.String() || env.Payload.OutletID != outletID.String() {
		t.Fatalf("envelope ids not decoded: %+v", env)
	}
	if len(env.Payload.Lines) != 2 || float64(env.Payload.Lines[0].Quantity) != 2 || float64(env.Payload.Lines[0].UnitCost) != 9000 {
		t.Fatalf("line 0 not decoded: %+v", env.Payload.Lines)
	}
	if float64(env.Payload.Lines[1].Quantity) != 30.5 || float64(env.Payload.Lines[1].UnitCost) != 45 {
		t.Fatalf("numeric line not decoded: %+v", env.Payload.Lines[1])
	}
}

// TestPlanProcurementOrdersOnlyShortfall: stock covers what it can (shared across lines of one
// item), only the rest is bought, and an unresolved goods line is bought in full.
func TestPlanProcurementOrdersOnlyShortfall(t *testing.T) {
	dvr, camera := &ent.Item{ID: uuid.New()}, &ent.Item{ID: uuid.New()}
	lines := []planLine{
		{item: dvr, quantity: 1, unitCost: 9000},
		{item: camera, quantity: 8, unitCost: 2500},
		{item: camera, quantity: 2, unitCost: 2500},
		{item: nil, quantity: 30, unitCost: 45}, // free-typed cable: no stock record
	}
	plan, fromStock, toBuy := planProcurement(lines, map[uuid.UUID]float64{dvr.ID: 3, camera.ID: 6})
	if plan[0].shortfall != 0 || plan[1].shortfall != 2 || plan[2].shortfall != 2 || plan[3].shortfall != 30 {
		t.Fatalf("unexpected shortfalls: %+v", plan)
	}
	if fromStock != 9000+6*2500 {
		t.Errorf("from stock = %v, want %v", fromStock, 9000+6*2500)
	}
	if toBuy != 4*2500+30*45 {
		t.Errorf("to buy = %v, want %v", toBuy, 4*2500+30*45)
	}
}

// TestPlanProcurementNoStock: with nothing on hand (negative balances included) everything is bought.
func TestPlanProcurementNoStock(t *testing.T) {
	item := &ent.Item{ID: uuid.New()}
	plan, fromStock, toBuy := planProcurement([]planLine{{item: item, quantity: 4, unitCost: 10}}, map[uuid.UUID]float64{item.ID: -2})
	if plan[0].shortfall != 4 || fromStock != 0 || toBuy != 40 {
		t.Fatalf("want all 4 bought, got %+v from=%v buy=%v", plan[0], fromStock, toBuy)
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
