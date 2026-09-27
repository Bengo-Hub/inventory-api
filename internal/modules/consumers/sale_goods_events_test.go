package consumers

import (
	"encoding/json"
	"testing"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
)

// TestSaleDeltas: the change (positive = in) that brings a document's movements to its target.
func TestSaleDeltas(t *testing.T) {
	dvr, cam, cable := uuid.New(), uuid.New(), uuid.New()

	// First sync: everything leaves stock.
	d := saleDeltas(map[uuid.UUID]float64{dvr: 1, cam: 8}, nil)
	if d[dvr] != -1 || d[cam] != -8 || len(d) != 2 {
		t.Fatalf("first sync = %v", d)
	}
	// Same snapshot again: nothing moves.
	if d := saleDeltas(map[uuid.UUID]float64{dvr: 1, cam: 8}, map[uuid.UUID]float64{dvr: 1, cam: 8}); len(d) != 0 {
		t.Fatalf("repeat moved stock: %v", d)
	}
	// Edited: cameras 8 -> 6, cable added, DVR gone.
	d = saleDeltas(map[uuid.UUID]float64{cam: 6, cable: 30}, map[uuid.UUID]float64{dvr: 1, cam: 8})
	if d[cam] != 2 || d[cable] != -30 || d[dvr] != 1 {
		t.Fatalf("edit = %v, want cam +2, cable -30, dvr +1", d)
	}
	// Voided / removed: all back.
	d = saleDeltas(nil, map[uuid.UUID]float64{cam: 6})
	if d[cam] != 6 {
		t.Fatalf("void = %v", d)
	}
	// Credit note: goods come back in (negative target).
	d = saleDeltas(map[uuid.UUID]float64{cam: -2}, nil)
	if d[cam] != 2 {
		t.Fatalf("credit note = %v, want +2", d)
	}
}

func TestSaleDocReference(t *testing.T) {
	root, doc := uuid.New(), uuid.New()
	for kind, short := range map[string]string{"invoice": "inv", "delivery_note": "dn", "credit_note": "cn", "bought": "bought"} {
		if got, want := saleDocReference(root, kind, doc), "sale-"+root.String()+":"+short+"-"+doc.String(); got != want {
			t.Fatalf("%s reference = %s, want %s", kind, got, want)
		}
	}
}

// TestParseSaleSnapshot decodes the envelope treasury emits (decimal quantities as strings).
func TestParseSaleSnapshot(t *testing.T) {
	tenantID, root, doc := uuid.New(), uuid.New(), uuid.New()
	payload := map[string]any{
		"root_id": root.String(), "root_number": "QT-1", "policy": "delivery", "customer_name": "Acme",
		"documents": []map[string]any{{
			"kind": "delivery_note", "document_id": doc.String(), "document_number": "DC-1",
			"lines": []map[string]any{{"item_id": uuid.NewString(), "sku": "X", "quantity": "2.5"}},
		}},
	}
	data, err := eventslib.NewEvent("sale_goods_issued", "treasury", uuid.New(), tenantID, payload).ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		TenantID string       `json:"tenant_id"`
		Payload  saleSnapshot `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.TenantID != tenantID.String() || env.Payload.Policy != "delivery" || len(env.Payload.Documents) != 1 ||
		float64(env.Payload.Documents[0].Lines[0].Quantity) != 2.5 {
		t.Fatalf("snapshot not decoded: %+v", env)
	}
}
