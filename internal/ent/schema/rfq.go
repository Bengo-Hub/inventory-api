package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RFQ (request for quotation) sources competitive quotes from multiple suppliers
// before committing purchase orders. Lifecycle: draft → sent → closed → awarded.
type RFQ struct {
	ent.Schema
}

func (RFQ) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.String("rfq_number").NotEmpty().Comment("Unique RFQ number per tenant"),
		field.String("title").Optional(),
		field.Enum("status").Values("draft", "sent", "closed", "awarded", "cancelled").Default("draft"),
		field.UUID("requisition_id", uuid.UUID{}).Optional().Nillable().Comment("Originating requisition, if any"),
		field.UUID("warehouse_id", uuid.UUID{}).Optional().Nillable().Comment("Destination warehouse for resulting POs"),
		field.Text("notes").Optional(),
		field.Time("due_date").Optional().Nillable().Comment("Quote submission deadline"),
		field.UUID("created_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (RFQ) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("lines", RFQLine.Type),
		edge.To("responses", SupplierResponse.Type),
		edge.To("awards", RFQAward.Type),
	}
}

func (RFQ) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "rfq_number").Unique(),
		index.Fields("tenant_id", "status"),
	}
}
