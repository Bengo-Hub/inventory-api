package handlers

import (
	"net/http"
	"time"
)

// parseCreatedAtRange parses optional ?from=&to= (YYYY-MM-DD) query params for list endpoints
// that filter on CreatedAt — a plain UTC calendar-day range. `to` is end-of-day inclusive. ok is
// false when neither param is present (caller applies no date filter at all). Mirrors pos-api's
// handlers.parseCreatedAtRange so both services' list endpoints share the same query convention.
func parseCreatedAtRange(r *http.Request) (from, to time.Time, ok bool) {
	fromStr, toStr := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fromStr == "" && toStr == "" {
		return time.Time{}, time.Time{}, false
	}
	if fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = t
		}
	}
	if toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			to = t.Add(24*time.Hour - time.Second)
		}
	}
	if to.IsZero() {
		to = time.Now().UTC()
	}
	return from, to, true
}
