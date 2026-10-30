package items

import (
	"context"
	"database/sql"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/bulkjobs"
)

// TaxCodeJobType is the bulk_jobs.job_type of a catalog tax-code job.
const TaxCodeJobType = "item_tax_codes"

// QueueTenantTaxCodes plans a tax-code job and, when it would change any item, runs it as a
// background bulk job (persisted, with the bulk_job.completed push to the tenant's sessions).
// Returns the queued job, or nil when there is nothing to change.
func (s *Service) QueueTenantTaxCodes(ctx context.Context, jobs *bulkjobs.Service, tenantID uuid.UUID, mode string, createdBy uuid.UUID) (*ent.BulkJob, *TaxCodeJobPlan, error) {
	plan, err := s.PlanTenantTaxCodes(ctx, tenantID, mode)
	if err != nil {
		return nil, nil, err
	}
	if plan.Total == 0 {
		return nil, plan, nil
	}
	if jobs == nil { // no job service wired (tests): run inline
		_, err := s.ApplyTenantTaxCodes(ctx, tenantID, plan)
		return nil, plan, err
	}
	payload := map[string]any{
		"mode": plan.Mode, "vat_active": plan.VATActive,
		"fill_code": plan.FillCode, "move_code": plan.MoveCode,
	}
	job, err := jobs.CreateAndRun(ctx, tenantID, nil, TaxCodeJobType, plan.Total, payload, createdBy,
		func(ctx context.Context, _ *ent.BulkJob) (bulkjobs.RunResult, error) {
			n, err := s.ApplyTenantTaxCodes(ctx, tenantID, plan)
			return bulkjobs.RunResult{Processed: n, Detail: payload}, err
		})
	return job, plan, err
}

// TaxCodeReconciler runs TaxJobReconcile for every tenant on startup and then daily: it gives
// new or imported items without a code their automatic code, and catches a VAT switch whose
// treasury event this service missed. One replica per day does the work.
type TaxCodeReconciler struct {
	svc  *Service
	jobs *bulkjobs.Service
	db   *sql.DB
	log  *zap.Logger
}

// NewTaxCodeReconciler builds the daily reconcile.
func NewTaxCodeReconciler(svc *Service, jobs *bulkjobs.Service, db *sql.DB, log *zap.Logger) *TaxCodeReconciler {
	return &TaxCodeReconciler{svc: svc, jobs: jobs, db: db, log: log.Named("items.TaxCodeReconciler")}
}

// Start runs a pass shortly after startup, then every 24 hours, until ctx is cancelled.
func (r *TaxCodeReconciler) Start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(2 * time.Minute) // let treasury and the outbox settle after a deploy
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				r.run(ctx)
				timer.Reset(24 * time.Hour)
			}
		}
	}()
}

func (r *TaxCodeReconciler) run(ctx context.Context) {
	if r.db == nil || !sharedcache.ClaimPeriod(ctx, "inventory:tax-code-reconcile", 23*time.Hour) {
		return
	}
	tenants, err := r.svc.TenantsWithItems(ctx)
	if err != nil {
		r.log.Warn("tax-code reconcile: list tenants", zap.Error(err))
		return
	}
	for _, t := range tenants {
		if ctx.Err() != nil {
			return
		}
		// Sequential on purpose: one tenant's batches at a time keeps the event outbox and the
		// POS/ordering consumers at a steady pace.
		plan, err := r.svc.PlanTenantTaxCodes(ctx, t, TaxJobReconcile)
		if err != nil {
			r.log.Debug("tax-code reconcile: skipped tenant", zap.String("tenant", t.String()), zap.Error(err))
			continue
		}
		if plan.Total == 0 {
			continue
		}
		n, err := r.svc.ApplyTenantTaxCodes(ctx, t, plan)
		if err != nil {
			r.log.Warn("tax-code reconcile failed", zap.String("tenant", t.String()), zap.Int("updated", n), zap.Error(err))
			continue
		}
		r.log.Info("tax-code reconcile applied", zap.String("tenant", t.String()), zap.Int("updated", n),
			zap.String("fill_code", plan.FillCode), zap.String("move_code", plan.MoveCode), zap.Bool("vat_active", plan.VATActive))
	}
}
