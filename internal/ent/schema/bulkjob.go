package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// BulkJob tracks a background bulk operation (item relocation, bulk stock adjustment, and any
// future bulk action that adopts the same pattern) so a large batch runs off the request/response
// cycle with bounded concurrency, progress tracking, and a completion notification — instead of
// blocking one HTTP call for however long the whole batch takes with no visibility. See
// internal/modules/bulkjobs for the runner.
type BulkJob struct {
	ent.Schema
}

// Fields of the BulkJob.
func (BulkJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("job_type").
			NotEmpty().
			Comment("e.g. item_relocation, bulk_stock_adjust — free-form, not an enum, so a new bulk action can adopt this without a migration"),
		field.Enum("status").
			Values("queued", "running", "completed", "failed").
			Default("queued"),
		field.Int("total").
			Default(0).
			Comment("Total line items in the job"),
		field.Int("processed").
			Default(0).
			Comment("Successfully applied line items so far"),
		field.Int("failed_count").
			Default(0).
			Comment("Skipped/failed line items so far"),
		field.JSON("payload", map[string]any{}).
			Optional().
			Comment("The original request, for the runner to replay/inspect"),
		field.JSON("result", map[string]any{}).
			Optional().
			Comment("Per-line skip/failure details once the job finishes"),
		field.UUID("created_by", uuid.UUID{}).
			Optional().
			Nillable(),
		field.Text("error").
			Optional().
			Comment("Set when status=failed and the job aborted entirely (not per-line skips, which live in result)"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("started_at").
			Optional().
			Nillable(),
		field.Time("completed_at").
			Optional().
			Nillable(),
	}
}

// Indexes of the BulkJob.
func (BulkJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "created_at"),
		index.Fields("tenant_id", "status"),
	}
}
