package consumers

import "testing"

// The idempotency prefix must depend only on the return, never on which subject delivered it,
// so a resync (pos.return.restock_requested) dedupes against the original completion event.
func TestReturnSource(t *testing.T) {
	cases := []struct {
		returnType, subject, wantSource, wantPrefix string
	}{
		{"refund", "pos.return.completed", "pos", "pos-return-"},
		{"store_credit", "pos.return.restock_requested", "pos", "pos-return-"},
		{"exchange", "pos.exchange.completed", "pos_exchange", "pos-exchange-"},
		{"exchange", "pos.return.restock_requested", "pos_exchange", "pos-exchange-"},
		{"", "pos.exchange.completed", "pos_exchange", "pos-exchange-"},
		{"", "pos.return.completed", "pos", "pos-return-"},
	}
	for _, c := range cases {
		src, prefix := returnSource(c.returnType, c.subject)
		if src != c.wantSource || prefix != c.wantPrefix {
			t.Errorf("returnSource(%q,%q) = %q,%q; want %q,%q", c.returnType, c.subject, src, prefix, c.wantSource, c.wantPrefix)
		}
	}
}
