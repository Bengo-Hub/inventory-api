package consumers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// A linked buyer arrives as crm_contact_id only; anything that is not a real id means no link.
func TestParseCRMContactID(t *testing.T) {
	valid := uuid.New()
	cases := []struct {
		name string
		raw  string
		want *uuid.UUID
	}{
		{"valid", valid.String(), &valid},
		{"valid with spaces", " " + valid.String() + " ", &valid},
		{"empty", "", nil},
		{"invalid", "not-a-uuid", nil},
		{"nil uuid", uuid.Nil.String(), nil},
	}
	for _, c := range cases {
		got := parseCRMContactID(c.raw)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %v, want nil", c.name, *got)
		case c.want != nil && (got == nil || *got != *c.want):
			t.Errorf("%s: got %v, want %v", c.name, got, *c.want)
		}
	}
}

// The return payload decodes crm_contact_id next to the existing customer_name.
func TestReturnEventPayload_CRMContactID(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name, body string
		want       *uuid.UUID
		wantName   string
	}{
		{"linked buyer", `{"return_id":"x","crm_contact_id":"` + id.String() + `"}`, &id, ""},
		{"unlinked buyer", `{"return_id":"x","customer_name":"Jane"}`, nil, "Jane"},
		{"anonymous", `{"return_id":"x"}`, nil, ""},
		{"invalid id", `{"return_id":"x","crm_contact_id":"junk"}`, nil, ""},
	}
	for _, c := range cases {
		var p returnEventPayload
		if err := json.Unmarshal([]byte(c.body), &p); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		got := parseCRMContactID(p.CrmContactID)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: contact id = %v, want %v", c.name, got, c.want)
		}
		if p.CustomerName != c.wantName {
			t.Errorf("%s: customer name = %q, want %q", c.name, p.CustomerName, c.wantName)
		}
	}
}

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
