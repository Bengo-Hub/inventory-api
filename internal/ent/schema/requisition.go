package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Requisition is a procurement request (migrated from ERP procurement.requisitions
// ProcurementRequest). Workflow: draft → submitted → procurement_review → approved →
// rejected/ordered → completed. Approved requisitions convert to PurchaseOrders.
type Requisition struct {
	ent.Schema
}

func (Requisition) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.UUID("outlet_id", uuid.UUID{}).Optional().Nillable().Comment("Branch/outlet this requisition is for"),
		field.String("reference_number").NotEmpty().Comment("Unique requisition reference per tenant"),
		field.UUID("requester_id", uuid.UUID{}).Optional().Nillable().Comment("Auth user who raised the requisition"),
		field.Enum("request_type").Values("inventory", "external_item", "service").Default("inventory"),
		field.Text("purpose").Optional(),
		field.Enum("priority").Values("low", "medium", "high", "critical").Default("medium"),
		field.Time("required_by_date").Optional().Nillable(),
		field.Enum("status").
			Values("draft", "submitted", "procurement_review", "approved", "rejected", "ordered", "completed").
			Default("draft"),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Requisition) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("lines", RequisitionLine.Type),
	}
}

func (Requisition) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "reference_number").Unique(),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "request_type"),
		index.Fields("tenant_id", "priority"),
	}
}
