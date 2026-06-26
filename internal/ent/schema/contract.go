package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Contract is a supplier contract (migrated from ERP procurement.contracts.Contract).
type Contract struct{ ent.Schema }

func (Contract) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("supplier_id", uuid.UUID{}).Comment("FK to Supplier"),
		field.UUID("rfq_id", uuid.UUID{}).Optional().Nillable().Comment("Source RFQ this contract was awarded from"),
		// project_id flows from the source RFQ so an awarded supplier contract stays attributed to
		// the project (completes the Requisition → RFQ → contract project link).
		field.UUID("project_id", uuid.UUID{}).Optional().Nillable().Comment("projects-service project id (from the source RFQ)"),
		field.String("title").NotEmpty(),
		field.Time("start_date"),
		field.Time("end_date"),
		field.Float("value").Default(0),
		field.Enum("status").Values("draft", "active", "expired", "terminated").Default("draft"),
		field.Text("terms").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Contract) Edges() []ent.Edge {
	return []ent.Edge{edge.To("order_links", ContractOrderLink.Type)}
}

func (Contract) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "supplier_id"),
		index.Fields("tenant_id", "status"),
	}
}
