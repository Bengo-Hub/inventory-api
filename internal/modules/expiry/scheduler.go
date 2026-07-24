package expiry

import (
	"context"
	"database/sql"
	"time"

	"go.uber.org/zap"
)

// schedulerAdvisoryLockKey is a fixed, service-unique Postgres advisory lock key so only one
// replica runs the expiry scan per tick. ('I','N','E','X')
const schedulerAdvisoryLockKey int64 = 0x494E_4558

// SchedulerConfig configures the expiry-alert scan.
type SchedulerConfig struct {
	Enabled bool // EXPIRY_ALERT_SCHEDULE_ENABLED (default true)
}

// Scheduler runs the expiry-alert scan hourly (cheap cutoff query, same rationale as the EOL
// purge scheduler — no per-day alignment needed) via a timer loop, advisory-lock guarded so
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

// runGuarded acquires the advisory lock and, if won, runs the expiry scan across every
// tenant with notifications enabled.
func (sc *Scheduler) runGuarded(ctx context.Context) {
	if sc.db == nil {
		return
	}
	conn, err := sc.db.Conn(ctx)
	if err != nil {
		sc.log.Warn("expiry scheduler: acquire conn failed", zap.Error(err))
		return
	}
	defer func() { _ = conn.Close() }()

	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, schedulerAdvisoryLockKey).Scan(&got); err != nil {
		sc.log.Warn("expiry scheduler: advisory lock failed", zap.Error(err))
		return
	}
	if !got {
		return
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schedulerAdvisoryLockKey)
	}()

	alerted, err := sc.svc.RunExpiryCheck(ctx)
	if err != nil {
		sc.log.Warn("expiry scheduler: run failed", zap.Error(err))
		return
	}
	if alerted > 0 {
		sc.log.Info("expiry alert scan complete", zap.Int("new_alerts", alerted))
	}
}
