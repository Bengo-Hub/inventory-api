package items

import (
	"context"
	"database/sql"
	"time"

	"go.uber.org/zap"
)

// eolPurgeAdvisoryLockKey is a fixed, service-unique key for the Postgres session advisory lock
// that guards the daily EOL purge so only ONE replica executes it. ('I','N','E','O')
const eolPurgeAdvisoryLockKey int64 = 0x494E_454F

// EOLPurgeConfig configures the daily End-of-Life hard-delete purge.
type EOLPurgeConfig struct {
	Enabled       bool // EOL_PURGE_ENABLED (default true)
	RetentionDays int  // EOL_RETENTION_DAYS (default 7)
}

// EOLPurgeScheduler hard-deletes items whose End-of-Life retention window has elapsed. It runs on
// a time-until-next-hour timer loop (no external cron dep) and uses a Postgres advisory lock so
// only one replica performs the work — mirroring the backup scheduler pattern.
type EOLPurgeScheduler struct {
	svc *Service
	db  *sql.DB
	cfg EOLPurgeConfig
	log *zap.Logger
}

// NewEOLPurgeScheduler builds the scheduler. Zero-value config gets sane defaults.
func NewEOLPurgeScheduler(svc *Service, db *sql.DB, cfg EOLPurgeConfig, log *zap.Logger) *EOLPurgeScheduler {
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = DefaultEOLRetentionDays
	}
	return &EOLPurgeScheduler{svc: svc, db: db, cfg: cfg, log: log.Named("items.EOLPurgeScheduler")}
}

// Start launches the scheduler goroutine: a purge on startup, then hourly (the purge itself is a
// cheap cutoff query, so an hourly wake keeps deletions timely without a per-day alignment). Stops
// when ctx is cancelled.
func (sc *EOLPurgeScheduler) Start(ctx context.Context) {
	if !sc.cfg.Enabled {
		sc.log.Info("EOL purge scheduler disabled (EOL_PURGE_ENABLED=false)")
		return
	}
	sc.log.Info("EOL purge scheduler started", zap.Int("retention_days", sc.cfg.RetentionDays))

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

// runGuarded acquires the advisory lock and, if won, purges expired EOL items. Only one replica
// wins the lock per tick.
func (sc *EOLPurgeScheduler) runGuarded(ctx context.Context) {
	if sc.db == nil {
		return
	}
	conn, err := sc.db.Conn(ctx)
	if err != nil {
		sc.log.Warn("eol purge: acquire conn failed", zap.Error(err))
		return
	}
	defer func() { _ = conn.Close() }()

	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, eolPurgeAdvisoryLockKey).Scan(&got); err != nil {
		sc.log.Warn("eol purge: advisory lock failed", zap.Error(err))
		return
	}
	if !got {
		return
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, eolPurgeAdvisoryLockKey)
	}()

	if _, _, err := sc.svc.PurgeExpiredEOL(ctx, sc.cfg.RetentionDays); err != nil {
		sc.log.Warn("eol purge run failed", zap.Error(err))
	}
}
