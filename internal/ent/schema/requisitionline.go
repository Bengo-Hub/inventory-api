package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RequisitionLine is a line on a Requisition (migrated from ERP procurement
// requisitions.RequestItem). Handles inventory items, external items, and services.
type RequisitionLine struct {
	ent.Schema
}

func (RequisitionLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.UUID("requisition_id", uuid.UUID{}).Comment("FK to Requisition"),
		field.Enum("item_type").Values("inventory", "external", "service").Default("inventory"),
		// Inventory item reference (when item_type=inventory). Loose UUID ref to Item.
		field.UUID("item_id", uuid.UUID{}).Optional().Nillable().Comment("FK to Item (inventory type)"),
		field.Int("quantity").Default(1),
		field.Int("approved_quantity").Optional().Nillable(),
		field.Bool("urgent").Default(false),
		// External item fields
		field.Text("description").Optional(),
		field.Text("specifications").Optional(),
		field.Float("estimated_price").Optional().Nillable(),
		field.UUID("supplier_id", uuid.UUID{}).Optional().Nillable().Comment("Preferred supplier (external/service)"),
		// Service fields
		field.Text("service_description").Optional(),
		field.Text("expected_deliverables").Optional(),
		field.String("duration").Optional(),
		field.Time("start_date").Optional().Nillable(),
		field.Time("end_date").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (RequisitionLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("requisition", Requisition.Type).
			Ref("lines").
			Field("requisition_id").
			Unique().
			Required(),
	}
}

func (RequisitionLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "requisition_id"),
		index.Fields("item_type"),
		index.Fields("item_id"),
	}
}
