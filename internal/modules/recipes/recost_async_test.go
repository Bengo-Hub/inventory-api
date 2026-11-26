package recipes

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecostBackoffDoublesAndStaysBounded(t *testing.T) {
	if got := recostBackoff(1); got != recostBaseBackoff {
		t.Fatalf("first retry = %v", got)
	}
	if got := recostBackoff(2); got != 2*recostBaseBackoff {
		t.Fatalf("second retry = %v", got)
	}
	if got := recostBackoff(0); got != recostBaseBackoff {
		t.Fatalf("non-positive attempt = %v", got)
	}
	var total time.Duration
	for n := 1; n < recostAttempts; n++ {
		total += recostBackoff(n)
	}
	if total >= recostTimeout {
		t.Fatalf("retries wait %v, longer than the run timeout %v", total, recostTimeout)
	}
}

func TestUniqueIDs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	got := uniqueIDs([]uuid.UUID{a, uuid.Nil, b, a, b})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("uniqueIDs = %v", got)
	}
	if got := uniqueIDs(nil); len(got) != 0 {
		t.Fatalf("uniqueIDs(nil) = %v", got)
	}
}
