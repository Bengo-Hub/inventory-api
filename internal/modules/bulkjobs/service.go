// Package bulkjobs runs a bulk operation (item relocation, bulk stock adjustment, and any future
// bulk action that adopts the same pattern) off the HTTP request/response cycle: the caller gets
// a job id back immediately, the actual per-item work runs in a detached goroutine with bounded
// concurrency (see RunBounded), and the initiating tenant is notified over the existing real-time
// WebSocket hub the moment it finishes — instead of one HTTP call blocking for however long a
// large batch takes, with no progress visibility and no cap on how much concurrent DB/cascade/GL
// work a single request can trigger at once.
package bulkjobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/bulkjob"
	"github.com/bengobox/inventory-service/internal/modules/notifications"
)

// RunResult is what a Runner reports back for one job: how many lines succeeded, how many were
// skipped/failed, and a JSON-able detail blob (typically {"skipped": [...]}) for the job's
// `result` column.
type RunResult struct {
	Processed int
	Failed    int
	Detail    map[string]any
}

// Runner performs the actual work for one job. It receives a context detached from the original
// HTTP request (so it isn't cancelled when the request returns). An error return marks the WHOLE
// job failed (status=failed, err.Error() stored on the job) — a runner that tolerates per-line
// failures (the expected case for a bulk action) should report those via RunResult.Failed/Detail
// instead of erroring the whole job.
type Runner func(ctx context.Context, job *ent.BulkJob) (RunResult, error)

// Service creates and runs bulk jobs.
type Service struct {
	client *ent.Client
	log    *zap.Logger
	hub    *notifications.Hub
}

// NewService creates a new bulk-jobs service. hub may be nil (tests, or a deployment without the
// notification hub wired) — job completion still persists to the DB, just without the WebSocket
// push; pollers (GetJob) still see the final state.
func NewService(client *ent.Client, log *zap.Logger, hub *notifications.Hub) *Service {
	return &Service{client: client, log: log.Named("bulkjobs"), hub: hub}
}

// CreateAndRun persists a queued job row, launches `run` in a detached goroutine, and returns the
// freshly-created (still-queued) job immediately — callers should respond 202 Accepted with the
// job id rather than waiting for it to finish.
func (s *Service) CreateAndRun(ctx context.Context, tenantID uuid.UUID, jobType string, total int, payload map[string]any, createdBy uuid.UUID, run Runner) (*ent.BulkJob, error) {
	create := s.client.BulkJob.Create().
		SetTenantID(tenantID).
		SetJobType(jobType).
		SetTotal(total).
		SetPayload(payload)
	if createdBy != uuid.Nil {
		create = create.SetCreatedBy(createdBy)
	}
	job, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}

	// Detached from the request context on purpose: the HTTP handler returns as soon as this
	// function returns, and the job must keep running after that.
	go s.execute(context.Background(), job, run)

	return job, nil
}

func (s *Service) execute(ctx context.Context, job *ent.BulkJob, run Runner) {
	now := time.Now()
	if _, err := s.client.BulkJob.UpdateOneID(job.ID).
		SetStatus(bulkjob.StatusRunning).
		SetStartedAt(now).
		Save(ctx); err != nil {
		s.log.Warn("mark job running failed", zap.Error(err), zap.String("job_id", job.ID.String()))
	}

	res, runErr := func() (r RunResult, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("panic: %v", rec)
			}
		}()
		return run(ctx, job)
	}()

	completedAt := time.Now()
	update := s.client.BulkJob.UpdateOneID(job.ID).
		SetProcessed(res.Processed).
		SetFailedCount(res.Failed).
		SetCompletedAt(completedAt)
	status := bulkjob.StatusCompleted
	if runErr != nil {
		status = bulkjob.StatusFailed
		update = update.SetError(runErr.Error())
		s.log.Error("bulk job failed", zap.Error(runErr), zap.String("job_id", job.ID.String()), zap.String("job_type", job.JobType))
	}
	if res.Detail != nil {
		update = update.SetResult(res.Detail)
	}
	updated, err := update.SetStatus(status).Save(ctx)
	if err != nil {
		s.log.Error("mark job completed failed", zap.Error(err), zap.String("job_id", job.ID.String()))
		return
	}

	if s.hub == nil {
		return
	}
	payload := map[string]any{
		"job_id":    updated.ID.String(),
		"job_type":  updated.JobType,
		"status":    string(updated.Status),
		"total":     updated.Total,
		"processed": updated.Processed,
		"failed":    updated.FailedCount,
	}
	if updated.CreatedBy != nil {
		payload["created_by"] = updated.CreatedBy.String()
	}
	s.hub.BroadcastToTenant(job.TenantID, notifications.Message{Type: "bulk_job.completed", Payload: payload})
}

// GetJob fetches a job by id, scoped to the tenant — the polling fallback for a client that
// isn't (or can't stay) connected to the notification WebSocket.
func (s *Service) GetJob(ctx context.Context, tenantID, jobID uuid.UUID) (*ent.BulkJob, error) {
	return s.client.BulkJob.Query().
		Where(bulkjob.ID(jobID), bulkjob.TenantID(tenantID)).
		Only(ctx)
}
