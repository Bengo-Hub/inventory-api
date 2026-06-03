package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SupplierPerformance is a supplier scorecard for a period (ERP SupplierPerformance).
type SupplierPerformance struct{ ent.Schema }

func (SupplierPerformance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("supplier_id", uuid.UUID{}).Comment("FK to Supplier"),
		field.Time("period_start"),
		field.Time("period_end"),
		field.Float("on_time_delivery_rate").Default(0),
		field.Float("defect_rate").Default(0),
		field.Float("average_lead_time_days").Default(0),
		field.Float("total_spend").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (SupplierPerformance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "supplier_id"),
		index.Fields("supplier_id", "period_start", "period_end"),
	}
}
