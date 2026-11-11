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
	return resolveBackdate(dateStr, now, "an adjustment", ErrInvalidAdjustmentDate)
}

// ErrInvalidReceivedDate is returned when a goods receipt's optional received_date is malformed,
// in the future, or older than maxAdjustmentBackDays. Handlers map it to 400.
var ErrInvalidReceivedDate = errors.New("invalid received_date")

// ResolveReceivedDate turns a goods receipt's optional "YYYY-MM-DD" received_date (the day the
// goods actually arrived) into the received_date to store, by the same rules as an adjustment:
// blank or today keeps the real time, an earlier day is the start of that day (UTC), the future and
// more than a year back are refused. Goods received on 7 Nov and entered on 9 Nov then sit on
// 7 Nov in stock history, the lot and the vendor bill; created_at keeps the real entry time.
func ResolveReceivedDate(dateStr string, now time.Time) (time.Time, error) {
	return resolveBackdate(dateStr, now, "a goods receipt", ErrInvalidReceivedDate)
}

// resolveBackdate is the shared rule behind ResolveAdjustmentDate and ResolveReceivedDate.
func resolveBackdate(dateStr string, now time.Time, what string, invalid error) (time.Time, error) {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		return now, nil
	}
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: must be YYYY-MM-DD", invalid)
	}
	n := now.UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case d.After(today):
		return time.Time{}, fmt.Errorf("%w: %s cannot be dated in the future", invalid, what)
	case d.Equal(today):
		return now, nil
	case d.Before(today.AddDate(0, 0, -maxAdjustmentBackDays)):
		return time.Time{}, fmt.Errorf("%w: more than %d days before today", invalid, maxAdjustmentBackDays)
	}
	return d, nil
}

// IsBackdatedAdjustment reports whether an adjustment was entered later than the date it counts
// under (see backdatedAfter).
func IsBackdatedAdjustment(adjustedAt, createdAt time.Time) bool {
	return createdAt.Sub(adjustedAt) > backdatedAfter
}
