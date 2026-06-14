package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AuditLog is a centralized, append-only trail of sensitive/fraud-relevant
// actions (stock adjustments, transfers, write-offs, role/permission changes,
// user-outlet assignments, …). Entries are immutable once written.
type AuditLog struct {
	ent.Schema
}

// Fields of the AuditLog.
func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("outlet_id", uuid.UUID{}).Optional().Nillable().
			Comment("Outlet/warehouse scope of the action, when applicable"),
		field.UUID("actor_user_id", uuid.UUID{}).
			Comment("User who performed the action"),
		field.UUID("approver_user_id", uuid.UUID{}).Optional().Nillable().
			Comment("Manager who approved via step-up, when the action required approval"),
		field.String("action").NotEmpty().
			Comment("Dotted action code, e.g. stock.adjustment, role.permission_change"),
		field.String("entity_type").Optional().
			Comment("Type of the affected entity, e.g. stock_adjustment, role"),
		field.String("entity_id").Optional().
			Comment("Identifier of the affected entity"),
		field.Text("reason").Optional(),
		field.JSON("before_json", map[string]any{}).Optional(),
		field.JSON("after_json", map[string]any{}).Optional(),
		field.Float("amount").Optional().Nillable().
			Comment("Monetary or quantity magnitude of the action, when relevant"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Indexes of the AuditLog.
func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "created_at"),
		index.Fields("tenant_id", "outlet_id", "action"),
		index.Fields("tenant_id", "actor_user_id"),
	}
}
