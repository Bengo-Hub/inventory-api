package expiry

import (
	"context"
	"database/sql"
	sharedcache "github.com/Bengo-Hub/cache"
	"time"

	"go.uber.org/zap"
)

// SchedulerConfig configures the expiry-alert scan.
type SchedulerConfig struct {
	Enabled bool // EXPIRY_ALERT_SCHEDULE_ENABLED (default true)
}

// Scheduler runs the expiry-alert scan hourly (cheap cutoff query, same rationale as the EOL
// purge scheduler — no per-day alignment needed) via a timer loop, claim guarded (one replica per hour) so
// only one replica performs the work.
type Scheduler struct {
	svc *Service
	db  *sql.DB
	cfg SchedulerConfig
	log *zap.Logger
}

// NewScheduler builds the expiry-alert scheduler.
func NewScheduler(svc *Service, db *sql.DB, cfg SchedulerConfig, log *zap.Logger) *Scheduler {
	return &Scheduler{svc: svc, db: db, cfg: cfg, log: log.Named("expiry.Scheduler")}
}

// Start launches the scheduler goroutine: a scan on startup, then hourly. Stops when ctx is
// cancelled.
func (sc *Scheduler) Start(ctx context.Context) {
	if !sc.cfg.Enabled {
		sc.log.Info("expiry alert scheduler disabled (EXPIRY_ALERT_SCHEDULE_ENABLED=false)")
		return
	}
	sc.log.Info("expiry alert scheduler started")

	go func() {
		sc.runGuarded(ctx)
		for {
			next := time.Now().Truncate(time.Hour).Add(time.Hour)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				sc.runGuarded(ctx)
			}
		}
	}()
}

// runGuarded claims the hour (sharedcache.ClaimPeriod) and, if won, runs the expiry scan across every
// tenant with notifications enabled.
func (sc *Scheduler) runGuarded(ctx context.Context) {
	if sc.db == nil {
		return
	}
	// One replica per hour does the work. This used a session pg_try_advisory_lock, which
	// PgBouncer transaction pooling breaks (lock and unlock land on different backends).
	if !sharedcache.ClaimPeriod(ctx, "inventory:expiry-alerts", time.Hour) {
		return
	}

	alerted, err := sc.svc.RunExpiryCheck(ctx)
	if err != nil {
		sc.log.Warn("expiry scheduler: run failed", zap.Error(err))
		return
	}
	if alerted > 0 {
		sc.log.Info("expiry alert scan complete", zap.Int("new_alerts", alerted))
	}
}
