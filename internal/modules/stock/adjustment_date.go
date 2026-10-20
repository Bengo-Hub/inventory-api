package stock

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Backdated stock adjustments reuse two columns that already exist, so no schema change is needed:
// adjusted_at is the EFFECTIVE date the movement counts under (the ledger, the Adjustments list,
// reports and the books all order and filter on it), and created_at is the real entry time that
// ent stamps immutably. A transfer does the same with transfer_date (transfers.EffectiveTransferDate).

// ErrInvalidAdjustmentDate is returned when the optional adjustment_date is malformed, in the
// future, or older than maxAdjustmentBackDays. Handlers map it to 400.
var ErrInvalidAdjustmentDate = errors.New("invalid adjustment_date")

// maxAdjustmentBackDays bounds how far back an adjustment may be dated. Unlike a transfer, an
// adjustment is never post-dated: it changes the live balance the moment it is entered, so a
// future date would put it ahead of stock that has not moved yet.
const maxAdjustmentBackDays = 366

// backdatedAfter is how far adjusted_at must sit before created_at for a row to count as
// backdated. A backdated adjustment is dated at the start of a day before today, so it is always
// at least 24 hours earlier than its entry time; every other writer stamps both columns together.
const backdatedAfter = 12 * time.Hour

// ResolveAdjustmentDate turns the optional "YYYY-MM-DD" adjustment_date into the adjusted_at to
// store. Blank, or today (UTC), means no backdate and returns now unchanged, so the common case
// keeps its real clock time. An earlier day is stored as the start of that day (UTC), the same
// convention transfer_date uses (inventory-api has no per-tenant timezone).
func ResolveAdjustmentDate(dateStr string, now time.Time) (time.Time, error) {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		return now, nil
	}
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: must be YYYY-MM-DD", ErrInvalidAdjustmentDate)
	}
	n := now.UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case d.After(today):
		return time.Time{}, fmt.Errorf("%w: an adjustment cannot be dated in the future", ErrInvalidAdjustmentDate)
	case d.Equal(today):
		return now, nil
	case d.Before(today.AddDate(0, 0, -maxAdjustmentBackDays)):
		return time.Time{}, fmt.Errorf("%w: more than %d days before today", ErrInvalidAdjustmentDate, maxAdjustmentBackDays)
	}
	return d, nil
}

// IsBackdatedAdjustment reports whether an adjustment was entered later than the date it counts
// under (see backdatedAfter).
func IsBackdatedAdjustment(adjustedAt, createdAt time.Time) bool {
	return createdAt.Sub(adjustedAt) > backdatedAfter
}
