package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ManufacturingAnalytics is a per-tenant, per-day pre-aggregated snapshot of
// production KPIs (migrated from ERP manufacturing.ManufacturingAnalytics). The
// live dashboard still computes on the fly; this powers historical/trend queries.
type ManufacturingAnalytics struct {
	ent.Schema
}

func (ManufacturingAnalytics) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.String("date").Comment("Snapshot day, YYYY-MM-DD"),
		field.Int("total_batches").Default(0),
		field.Int("completed_batches").Default(0),
		field.Int("failed_batches").Default(0),
		field.Float("total_production_qty").Default(0),
		field.Float("total_raw_material_cost").Default(0),
		field.Float("total_labor_cost").Default(0),
		field.Float("total_overhead_cost").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ManufacturingAnalytics) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "date").Unique(),
	}
}
