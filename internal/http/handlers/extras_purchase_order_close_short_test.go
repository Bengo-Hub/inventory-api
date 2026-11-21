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
