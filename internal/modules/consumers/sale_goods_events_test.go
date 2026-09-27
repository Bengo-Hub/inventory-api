package consumers

import (
	"testing"

	"github.com/google/uuid"
)

func TestSaleGoodsDelta(t *testing.T) {
	dvr, cam, cable := uuid.New(), uuid.New(), uuid.New()
	none := map[uuid.UUID]float64{}

	// First issue: every invoiced quantity leaves stock.
	d := saleGoodsDelta(map[uuid.UUID]float64{dvr: 1, cam: 8}, none, none)
	if d[dvr] != -1 || d[cam] != -8 || len(d) != 2 {
		t.Fatalf("first issue = %v", d)
	}

	// Resend: already taken, nothing moves.
	if d := saleGoodsDelta(map[uuid.UUID]float64{dvr: 1, cam: 8}, none, map[uuid.UUID]float64{dvr: 1, cam: 8}); len(d) != 0 {
		t.Fatalf("resend moved stock: %v", d)
	}

	// A delivery note from the sales order already took 5 cameras: the invoice takes the other 3.
	d = saleGoodsDelta(map[uuid.UUID]float64{cam: 8}, map[uuid.UUID]float64{cam: 5}, none)
	if d[cam] != -3 {
		t.Fatalf("after delivery note = %v, want -3", d)
	}

	// Re-issued after an edit: cameras 8 -> 6, cable added, DVR removed.
	d = saleGoodsDelta(map[uuid.UUID]float64{cam: 6, cable: 30}, none, map[uuid.UUID]float64{dvr: 1, cam: 8})
	if d[cam] != 2 || d[cable] != -30 || d[dvr] != 1 {
		t.Fatalf("edit = %v, want cam +2, cable -30, dvr +1", d)
	}

	// Void: everything the invoice took goes back.
	d = saleGoodsDelta(none, none, map[uuid.UUID]float64{cam: 6, cable: 30})
	if d[cam] != 6 || d[cable] != 30 {
		t.Fatalf("void = %v", d)
	}
}

func TestSaleReferences(t *testing.T) {
	root, inv, dn := uuid.New(), uuid.New(), uuid.New()
	if got := saleInvoiceReference(root, inv); got != "sale-"+root.String()+":inv-"+inv.String() {
		t.Fatalf("invoice reference = %s", got)
	}
	if got := saleDeliveryReference(root, dn); got != "sale-"+root.String()+":dn-"+dn.String() {
		t.Fatalf("delivery reference = %s", got)
	}
}
