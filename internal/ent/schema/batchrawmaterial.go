package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// BatchRawMaterial is raw-material consumption for a ProductionBatch (ERP BatchRawMaterial).
type BatchRawMaterial struct{ ent.Schema }

func (BatchRawMaterial) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("production_batch_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).Comment("FK to Item (raw material)"),
		field.UUID("unit_id", uuid.UUID{}).Optional().Nillable(),
		field.Float("quantity").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (BatchRawMaterial) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("production_batch", ProductionBatch.Type).Ref("raw_materials").Field("production_batch_id").Unique().Required(),
	}
}

func (BatchRawMaterial) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "production_batch_id"),
		index.Fields("item_id"),
	}
}
