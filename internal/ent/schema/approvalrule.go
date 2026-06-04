package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalRule defines, per tenant and module, an amount band that requires a
// sequence of approval steps. When a document (purchase order / requisition) is
// submitted for approval, the active rule whose [min_amount, max_amount] band
// contains the document amount drives its approval workflow.
type ApprovalRule struct {
	ent.Schema
}

func (ApprovalRule) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.Enum("module").
			Values("purchase_order", "requisition").
			Comment("Document type this rule governs"),
		field.String("name").NotEmpty(),
		field.Float("min_amount").Default(0).Comment("Inclusive lower bound of the amount band"),
		field.Float("max_amount").Optional().Nillable().Comment("Inclusive upper bound; nil = unbounded (and above)"),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ApprovalRule) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("steps", ApprovalStep.Type),
	}
}

func (ApprovalRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "module", "is_active"),
	}
}
