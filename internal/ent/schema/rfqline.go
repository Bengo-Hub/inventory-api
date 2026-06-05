package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RFQLine is a single requested item/service on an RFQ. item_id is optional so
// free-text or external lines can be quoted too.
type RFQLine struct {
	ent.Schema
}

func (RFQLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("rfq_id", uuid.UUID{}).Comment("FK to RFQ"),
		field.UUID("item_id", uuid.UUID{}).Optional().Nillable().Comment("FK to Item; nil for free-text/external"),
		field.String("description").Optional(),
		field.Int("quantity").Default(1),
		field.String("uom").Optional().Comment("Unit of measure label"),
	}
}

func (RFQLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("rfq", RFQ.Type).Ref("lines").Field("rfq_id").Unique().Required(),
	}
}

func (RFQLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rfq_id"),
		index.Fields("tenant_id"),
	}
}
