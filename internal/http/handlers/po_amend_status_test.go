package handlers

import (
	"testing"

	"github.com/google/uuid"
)

// TestAmendedPOStatus pins when an amendment sends a purchase order back to draft: only a sent
// order whose promised figures (total or supplier) change. Cosmetic edits keep it sent.
func TestAmendedPOStatus(t *testing.T) {
	sup := uuid.New()
	other := uuid.New()
	cases := []struct {
		name        string
		current     string
		oldTotal    float64
		newTotal    float64
		oldSupplier *uuid.UUID
		newSupplier uuid.UUID
		want        string
		wantDraft   bool
	}{
		{"draft stays draft", "draft", 100, 500, &sup, other, "draft", false},
		{"sent, same total and supplier", "sent", 5360, 5360, &sup, sup, "sent", false},
		{"sent, rounding noise only", "sent", 5360, 5360.001, &sup, sup, "sent", false},
		{"sent, total changed", "sent", 5360, 6000, &sup, sup, "draft", true},
		{"sent, supplier changed", "sent", 5360, 5360, &sup, other, "draft", true},
		{"sent, supplier omitted keeps it", "sent", 5360, 5360, &sup, uuid.Nil, "sent", false},
	}
	for _, tc := range cases {
		got, draft := amendedPOStatus(tc.current, tc.oldTotal, tc.newTotal, tc.oldSupplier, tc.newSupplier)
		if got != tc.want || draft != tc.wantDraft {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", tc.name, got, draft, tc.want, tc.wantDraft)
		}
	}
}
