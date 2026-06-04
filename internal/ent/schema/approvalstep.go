package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalStep is one sequential sign-off stage of an ApprovalRule. Steps are
// ordered by sequence; a document advances to the next step only after the
// current one is approved by a user holding approver_role.
type ApprovalStep struct {
	ent.Schema
}

func (ApprovalStep) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant (denormalized for queries)"),
		field.UUID("rule_id", uuid.UUID{}).Comment("FK to ApprovalRule"),
		field.Int("sequence").Comment("1-based order of this step within the rule"),
		field.String("name").NotEmpty().Comment("Human label, e.g. 'Manager sign-off'"),
		field.String("approver_role").NotEmpty().Comment("Inventory role code allowed to approve this step"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ApprovalStep) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("rule", ApprovalRule.Type).
			Ref("steps").
			Field("rule_id").
			Unique().
			Required(),
	}
}

func (ApprovalStep) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rule_id", "sequence").Unique(),
		index.Fields("tenant_id"),
	}
}
