package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ServiceDelivery tracks delivery of a requested external service (ERP ServiceDelivery).
type ServiceDelivery struct{ ent.Schema }

func (ServiceDelivery) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("requisition_line_id", uuid.UUID{}).Optional().Nillable().Comment("FK to RequisitionLine"),
		field.UUID("provider_id", uuid.UUID{}).Optional().Nillable().Comment("FK to Supplier/provider"),
		field.Time("start_date"),
		field.Time("end_date"),
		field.Text("deliverables").Optional(),
		field.Enum("status").Values("scheduled", "in_progress", "completed", "delayed").Default("scheduled"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ServiceDelivery) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "status"),
		index.Fields("requisition_line_id"),
	}
}
