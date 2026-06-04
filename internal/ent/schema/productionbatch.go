package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ProductionBatch is a manufacturing work order producing goods from a recipe/formula
// (migrated from ERP manufacturing.ProductionBatch).
type ProductionBatch struct{ ent.Schema }

func (ProductionBatch) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("outlet_id", uuid.UUID{}).Optional().Nillable().Comment("Branch/outlet"),
		field.String("batch_number").NotEmpty(),
		field.UUID("recipe_id", uuid.UUID{}).Comment("FK to Recipe (BOM/formula)"),
		field.Time("scheduled_date"),
		field.Time("start_date").Optional().Nillable(),
		field.Time("end_date").Optional().Nillable(),
		field.Enum("status").Values("planned", "in_progress", "completed", "cancelled", "failed").Default("planned"),
		field.Float("planned_quantity").Default(0),
		field.Float("actual_quantity").Optional().Nillable(),
		field.Float("labor_cost").Default(0),
		field.Float("overhead_cost").Default(0),
		field.Float("scrap_quantity").Default(0).Comment("Output units lost to scrap/rework"),
		field.Float("unit_cost").Optional().Nillable().Comment("Computed cost per output unit at completion (material+labor+overhead)/actual"),
		field.Text("notes").Optional(),
		field.UUID("created_by", uuid.UUID{}).Optional().Nillable(),
		field.UUID("supervisor_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ProductionBatch) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("raw_materials", BatchRawMaterial.Type),
		edge.To("quality_checks", QualityCheck.Type),
	}
}

func (ProductionBatch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "batch_number").Unique(),
		index.Fields("tenant_id", "status"),
		index.Fields("recipe_id"),
	}
}
