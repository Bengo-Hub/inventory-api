package stock

import (
	"context"
	"errors"
	"testing"

	"github.com/Bengo-Hub/shared-service-client/crm"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeContacts struct {
	calls    int
	gotIDs   []uuid.UUID
	contacts map[uuid.UUID]crm.Contact
	err      error
}

func (f *fakeContacts) BatchGet(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]crm.Contact, error) {
	f.calls++
	f.gotIDs = append(f.gotIDs, ids...)
	return f.contacts, f.err
}

func idPtr(id uuid.UUID) *uuid.UUID { return &id }

func TestApplyContactNames(t *testing.T) {
	known, unknown := uuid.New(), uuid.New()
	rows := []MovementRow{
		{Counterparty: "Jane Shopper"},
		{Counterparty: "Walk-in"},
		{Counterparty: linkedBuyerFallback, crmContactID: idPtr(known)},
		{Counterparty: linkedBuyerFallback, crmContactID: idPtr(unknown)},
	}
	applyContactNames(rows, map[uuid.UUID]string{known: "Amina Otieno"})
	want := []string{"Jane Shopper", "Walk-in", "Amina Otieno", linkedBuyerFallback}
	for i, w := range want {
		if rows[i].Counterparty != w {
			t.Errorf("row %d counterparty = %q, want %q", i, rows[i].Counterparty, w)
		}
	}
}

func TestContactDisplayNames(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	got := contactDisplayNames(map[uuid.UUID]crm.Contact{
		a: {ID: a, FirstName: " Amina ", LastName: "Otieno "},
		b: {ID: b, FirstName: "Brian"},
		c: {ID: c, LastName: "Kamau"},
		d: {ID: d, Phone: "+254700000000"},
	})
	want := map[uuid.UUID]string{a: "Amina Otieno", b: "Brian", c: "Kamau"}
	if len(got) != len(want) {
		t.Fatalf("got %d names, want %d: %v", len(got), len(want), got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("name of %s = %q, want %q", id, got[id], w)
		}
	}
}

func TestResolveContactCounterparties(t *testing.T) {
	tenant := uuid.New()
	known, gone := uuid.New(), uuid.New()
	newRows := func() []MovementRow {
		return []MovementRow{
			{Counterparty: linkedBuyerFallback, crmContactID: idPtr(known)},
			{Counterparty: linkedBuyerFallback, crmContactID: idPtr(known)},
			{Counterparty: linkedBuyerFallback, crmContactID: idPtr(gone)},
			{Counterparty: "Walk-in"},
		}
	}

	t.Run("one batch call per page with distinct ids", func(t *testing.T) {
		f := &fakeContacts{contacts: map[uuid.UUID]crm.Contact{known: {ID: known, FirstName: "Amina", LastName: "Otieno"}}}
		s := (&Service{log: zap.NewNop()}).WithContactResolver(f)
		rows := newRows()
		s.resolveContactCounterparties(context.Background(), tenant, rows)
		if f.calls != 1 {
			t.Fatalf("BatchGet calls = %d, want 1", f.calls)
		}
		if len(f.gotIDs) != 2 {
			t.Fatalf("ids sent = %v, want 2 distinct", f.gotIDs)
		}
		want := []string{"Amina Otieno", "Amina Otieno", linkedBuyerFallback, "Walk-in"}
		for i, w := range want {
			if rows[i].Counterparty != w {
				t.Errorf("row %d counterparty = %q, want %q", i, rows[i].Counterparty, w)
			}
		}
	})

	t.Run("CRM failure keeps the fallback", func(t *testing.T) {
		f := &fakeContacts{err: errors.New("boom")}
		s := (&Service{log: zap.NewNop()}).WithContactResolver(f)
		rows := newRows()
		s.resolveContactCounterparties(context.Background(), tenant, rows)
		for i := 0; i < 3; i++ {
			if rows[i].Counterparty != linkedBuyerFallback {
				t.Errorf("row %d counterparty = %q, want fallback", i, rows[i].Counterparty)
			}
		}
	})

	t.Run("no resolver wired", func(t *testing.T) {
		s := &Service{log: zap.NewNop()}
		rows := newRows()
		s.resolveContactCounterparties(context.Background(), tenant, rows)
		if rows[0].Counterparty != linkedBuyerFallback || rows[3].Counterparty != "Walk-in" {
			t.Errorf("unexpected counterparties: %q, %q", rows[0].Counterparty, rows[3].Counterparty)
		}
	})

	t.Run("no linked rows makes no call", func(t *testing.T) {
		f := &fakeContacts{}
		s := (&Service{log: zap.NewNop()}).WithContactResolver(f)
		s.resolveContactCounterparties(context.Background(), tenant, []MovementRow{{Counterparty: "Walk-in"}})
		if f.calls != 0 {
			t.Errorf("BatchGet calls = %d, want 0", f.calls)
		}
	})
}
