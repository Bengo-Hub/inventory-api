package recipes

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// recostTimeout caps one background recost run, so a stuck query cannot hold a goroutine
	// and a pooled connection for good.
	recostTimeout = 2 * time.Minute
	// recostAttempts bounds the retries of a run that failed on a transient database error.
	recostAttempts = 3
	// recostBaseBackoff is the first retry delay; each later retry doubles it.
	recostBaseBackoff = 500 * time.Millisecond
)

// recostBackoff is the wait before retry n (1-based): base, 2*base, 4*base, ... Capped by the
// attempt count, so the total wait stays bounded.
func recostBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	return recostBaseBackoff << (n - 1)
}

// RecostAfterCommit recosts, in the background, every recipe that uses any of the given items
// (and their parent recipes, see RecalculateCostsForIngredients). Call it only after the change
// that moved the items' cost has committed, so the recost reads the new cost.
//
// The run is detached from ctx's cancellation: a request context ends when its response is
// written, which used to cancel the cascade before it finished. It keeps ctx's values and runs
// under its own timeout, with a bounded number of retries.
func (s *Service) RecostAfterCommit(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) {
	ids := uniqueIDs(itemIDs)
	if len(ids) == 0 {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		runCtx, cancel := context.WithTimeout(detached, recostTimeout)
		defer cancel()
		for attempt := 1; attempt <= recostAttempts; attempt++ {
			err := s.RecalculateCostsForIngredients(runCtx, tenantID, ids)
			if err == nil {
				return
			}
			if attempt == recostAttempts || runCtx.Err() != nil {
				s.log.Warn("recipes: background recost gave up",
					zap.String("tenant_id", tenantID.String()), zap.Int("items", len(ids)),
					zap.Int("attempts", attempt), zap.Error(err))
				return
			}
			select {
			case <-time.After(recostBackoff(attempt)):
			case <-runCtx.Done():
				return
			}
		}
	}()
}
