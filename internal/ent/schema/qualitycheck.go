package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// QualityCheck is a QC record for a ProductionBatch (ERP manufacturing.QualityCheck).
type QualityCheck struct{ ent.Schema }

func (QualityCheck) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("production_batch_id", uuid.UUID{}),
		field.UUID("inspector_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("check_date").Default(time.Now),
		field.Enum("result").Values("pass", "fail", "pending").Default("pending"),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (QualityCheck) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("production_batch", ProductionBatch.Type).Ref("quality_checks").Field("production_batch_id").Unique().Required(),
	}
}

func (QualityCheck) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "production_batch_id"),
		index.Fields("result"),
	}
}
