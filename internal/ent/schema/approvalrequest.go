package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalRequest is a live approval-workflow instance bound to a specific
// document (purchase order or requisition). It is created when a document is
// submitted for approval and a matching ApprovalRule with steps is found.
type ApprovalRequest struct {
	ent.Schema
}

func (ApprovalRequest) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.Enum("module").Values("purchase_order", "requisition"),
		field.UUID("object_id", uuid.UUID{}).Comment("ID of the PO/requisition being approved"),
		field.String("object_reference").Optional().Comment("PO number / requisition reference for display"),
		field.Float("amount").Default(0).Comment("Document amount snapshot at submission"),
		field.UUID("rule_id", uuid.UUID{}).Optional().Nillable().Comment("Rule that generated this request"),
		field.Enum("status").
			Values("pending", "approved", "rejected", "cancelled").
			Default("pending"),
		field.Int("current_sequence").Default(1).Comment("Sequence of the step awaiting action"),
		field.UUID("submitted_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("decided_at").Optional().Nillable().Comment("When the request reached a terminal state"),
	}
}

func (ApprovalRequest) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("actions", ApprovalAction.Type),
	}
}

func (ApprovalRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "module", "object_id"),
		index.Fields("tenant_id", "status"),
	}
}
