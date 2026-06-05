package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RFQAward records that a specific RFQ line was awarded to a supplier at a price.
// Partial awards across suppliers are supported (one award per line); converting
// awards to POs groups them by supplier and stamps po_id.
type RFQAward struct {
	ent.Schema
}

func (RFQAward) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("rfq_id", uuid.UUID{}).Comment("FK to RFQ"),
		field.UUID("rfq_line_id", uuid.UUID{}).Comment("Awarded RFQ line"),
		field.UUID("supplier_id", uuid.UUID{}).Comment("Winning supplier"),
		field.Float("unit_price").Default(0),
		field.Int("quantity").Default(1),
		field.UUID("po_id", uuid.UUID{}).Optional().Nillable().Comment("PO created from this award"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (RFQAward) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("rfq", RFQ.Type).Ref("awards").Field("rfq_id").Unique().Required(),
	}
}

func (RFQAward) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rfq_id"),
		index.Fields("rfq_id", "rfq_line_id").Unique(),
		index.Fields("tenant_id"),
	}
}
