package stock

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestResolveAdjustmentDate(t *testing.T) {
	now := time.Date(2026, 10, 20, 15, 30, 0, 0, time.UTC)
	cases := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{"blank keeps the real clock time", "", now, false},
		{"today is not a backdate", "2026-10-20", now, false},
		{"yesterday lands at the start of that day", "2026-10-19", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), false},
		{"a month back", "2026-09-20", time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), false},
		{"future is refused", "2026-10-21", time.Time{}, true},
		{"malformed is refused", "20/10/2026", time.Time{}, true},
		{"older than the window is refused", "2025-10-01", time.Time{}, true},
		{"oldest allowed day", "2025-10-19", time.Date(2025, 10, 19, 0, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		got, err := ResolveAdjustmentDate(c.in, now)
		if c.wantErr {
			if !errors.Is(err, ErrInvalidAdjustmentDate) {
				t.Errorf("%s: err = %v, want ErrInvalidAdjustmentDate", c.name, err)
			}
			continue
		}
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%s: got %v, %v want %v", c.name, got, err, c.want)
		}
	}
}

func TestIsBackdatedAdjustment(t *testing.T) {
	created := time.Date(2026, 10, 20, 0, 5, 0, 0, time.UTC)
	if IsBackdatedAdjustment(created, created) {
		t.Error("a normal adjustment (both columns stamped together) reads as backdated")
	}
	if IsBackdatedAdjustment(created.Add(-2*time.Second), created) {
		t.Error("seconds of clock skew read as backdated")
	}
	// Entered at 00:05 for the day before: the smallest gap a real backdate can have is 24h05m.
	if !IsBackdatedAdjustment(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), created) {
		t.Error("an adjustment dated the previous day does not read as backdated")
	}
}

// Stock 80 on 1 Aug, +20 on 1 Sep (100), +5 on 1 Oct (105). On 20 Oct a +10 is backdated to 15 Aug,
// so the live balance is 115 and the true figures are 90 on 15 Aug, 110 on 1 Sep and 115 on 1 Oct.
// The two adjustments written before the backdate stored 100 and 105; trusted as anchors they would
// put the ledger 10 short. After the shift the walk from live stock reproduces every true figure.
func TestShiftStaleAnchorsWithBackfill(t *testing.T) {
	wh, other := uuid.New(), uuid.New()
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	ptr := func(f float64) *float64 { return &f }
	entered := day(10, 20)
	rows := []MovementRow{ // newest first, as ItemStockHistory sorts them
		{Type: "adjustment", QuantityChange: 5, QuantityAfter: ptr(105), OccurredAt: day(10, 1), WarehouseID: &wh},
		{Type: "adjustment", QuantityChange: 20, QuantityAfter: ptr(100), OccurredAt: day(9, 1), WarehouseID: &wh},
		{Type: "adjustment", QuantityChange: 10, OccurredAt: day(8, 15), WarehouseID: &wh, EnteredAt: &entered},
		{Type: "adjustment", QuantityChange: 80, QuantityAfter: ptr(80), OccurredAt: day(8, 1), WarehouseID: &wh},
	}
	shiftStaleAnchors(rows)
	backfillQuantityAfter(rows, 115)

	want := []float64{115, 110, 90, 80}
	for i, w := range want {
		if rows[i].QuantityAfter == nil || *rows[i].QuantityAfter != w {
			t.Errorf("row %d new quantity = %v, want %v", i, rows[i].QuantityAfter, w)
		}
	}

	// Another warehouse's snapshot is per balance and must not move.
	foreign := []MovementRow{
		{Type: "adjustment", QuantityChange: 3, QuantityAfter: ptr(3), OccurredAt: day(9, 1), WarehouseID: &other},
		{Type: "adjustment", QuantityChange: 10, OccurredAt: day(8, 15), WarehouseID: &wh, EnteredAt: &entered},
	}
	shiftStaleAnchors(foreign)
	if got := *foreign[0].QuantityAfter; got != 3 {
		t.Errorf("another warehouse's anchor moved to %v", got)
	}
}

// With no backdated row in the ledger nothing is touched.
func TestShiftStaleAnchorsNoBackdate(t *testing.T) {
	wh := uuid.New()
	v := 50.0
	rows := []MovementRow{{Type: "adjustment", QuantityChange: 5, QuantityAfter: &v, OccurredAt: time.Now(), WarehouseID: &wh}}
	shiftStaleAnchors(rows)
	if *rows[0].QuantityAfter != 50 {
		t.Errorf("anchor changed to %v", *rows[0].QuantityAfter)
	}
}
