package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

// Ordered 10, the supplier sent 6 and will not send the rest: closing short records 4 not
// delivered, without touching stock (owner, 20 November 2026).
func TestPOShortfall(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	lines := []*ent.PurchaseOrderLine{
		{ItemID: a, QuantityOrdered: 10, QuantityReceived: 6, UnitPrice: 50},
		{ItemID: b, QuantityOrdered: 5, QuantityReceived: 5, UnitPrice: 20},
	}
	short, anyReceived := poShortfall(lines)
	if !anyReceived {
		t.Fatal("6 were received")
	}
	if len(short) != 1 || short[0].ItemID != a || short[0].Missing != 4 {
		t.Fatalf("shortfall = %+v, want one line missing 4", short)
	}
	if got := shortfallValue(short); got != 200 {
		t.Fatalf("shortfall value = %v, want 200", got)
	}

	none, anyReceived := poShortfall([]*ent.PurchaseOrderLine{{ItemID: a, QuantityOrdered: 3}})
	if anyReceived || len(none) != 1 {
		t.Fatalf("nothing received: anyReceived=%v short=%+v", anyReceived, none)
	}
}

func TestCloseShortNote(t *testing.T) {
	day := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	short := []poShortLine{{ItemID: uuid.New(), Ordered: 10, Received: 6, Missing: 4}}
	note := closeShortNote("Previous note", short, map[uuid.UUID]string{short[0].ItemID: "Sugar 1kg"}, "supplier out of stock", day)
	for _, want := range []string{"Previous note", "Closed short on 2026-11-21", "Sugar 1kg: 6 of 10 received, 4 not delivered", "supplier out of stock"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q is missing %q", note, want)
		}
	}
}

// One item of the order never came (0 of 100): closing it on the receipt settles the line, and
// a line already closed is not outstanding any more (client report, 26 November 2026).
func TestLineOutstanding(t *testing.T) {
	cases := []struct {
		l    ent.PurchaseOrderLine
		want float64
	}{
		{ent.PurchaseOrderLine{QuantityOrdered: 100}, 100},
		{ent.PurchaseOrderLine{QuantityOrdered: 100, QuantityReceived: 60}, 40},
		{ent.PurchaseOrderLine{QuantityOrdered: 100, QuantityClosed: 100}, 0},
		{ent.PurchaseOrderLine{QuantityOrdered: 100, QuantityReceived: 60, QuantityClosed: 40}, 0},
		{ent.PurchaseOrderLine{QuantityOrdered: 10, QuantityReceived: 12}, 0},
	}
	for _, c := range cases {
		l := c.l
		if got := lineOutstanding(&l); got != c.want {
			t.Fatalf("outstanding(%+v) = %v, want %v", c.l, got, c.want)
		}
	}
}

func TestValidateReceiptClosures(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	la, lb := uuid.New(), uuid.New()
	po := []*ent.PurchaseOrderLine{
		{ID: la, ItemID: a, QuantityOrdered: 100},
		{ID: lb, ItemID: b, QuantityOrdered: 20},
	}
	// Item b arrived, item a never came and is closed: allowed.
	ok := []grnLinePayload{
		{PurchaseOrderLineID: &la, ItemID: a, CloseRemaining: true},
		{PurchaseOrderLineID: &lb, ItemID: b, QuantityReceived: 20},
	}
	if code, _ := validateReceiptClosures(ok, po); code != "" {
		t.Fatalf("receive b and close a: got %s", code)
	}
	// Nothing arrived now or before: the order is cancelled, not closed line by line.
	nothing := []grnLinePayload{{PurchaseOrderLineID: &la, ItemID: a, CloseRemaining: true}}
	if code, _ := validateReceiptClosures(nothing, po); code != "NOTHING_RECEIVED" {
		t.Fatalf("nothing received: got %q", code)
	}
	// The same, but b came on an earlier receipt: closing a now settles the order.
	earlier := []*ent.PurchaseOrderLine{po[0], {ID: lb, ItemID: b, QuantityOrdered: 20, QuantityReceived: 20}}
	if code, _ := validateReceiptClosures(nothing, earlier); code != "" {
		t.Fatalf("close after an earlier receipt: got %s", code)
	}
	// A closed line must point at its own order line.
	stray := uuid.New()
	for _, bad := range [][]grnLinePayload{
		{{ItemID: a, CloseRemaining: true, QuantityReceived: 1}},
		{{PurchaseOrderLineID: &stray, ItemID: a, CloseRemaining: true, QuantityReceived: 1}},
		{{PurchaseOrderLineID: &lb, ItemID: a, CloseRemaining: true, QuantityReceived: 1}},
	} {
		if code, _ := validateReceiptClosures(bad, po); code != "CLOSE_NEEDS_ORDER_LINE" {
			t.Fatalf("bad closure %+v: got %q", bad, code)
		}
	}
}
