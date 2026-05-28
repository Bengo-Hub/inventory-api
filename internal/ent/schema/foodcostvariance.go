package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// FoodCostVariance stores the result of a period food cost variance calculation.
type FoodCostVariance struct {
	ent.Schema
}

// Fields of the FoodCostVariance.
func (FoodCostVariance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.Time("period_start"),
		field.Time("period_end"),
		field.String("recipe_sku").
			NotEmpty(),
		field.String("recipe_name").
			NotEmpty(),
		field.Float("theoretical_cost").
			Comment("Expected cost based on BOM × units sold"),
		field.Float("actual_cost").
			Comment("Actual inventory consumption cost for the period"),
		field.Float("variance_pct").
			Comment("((actual - theoretical) / theoretical) × 100"),
		field.JSON("breakdown", map[string]any{}).
			Optional(),
		field.Time("calculated_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the FoodCostVariance — none needed.
func (FoodCostVariance) Edges() []ent.Edge {
	return nil
}

// Indexes of the FoodCostVariance.
func (FoodCostVariance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "period_start", "period_end"),
		index.Fields("tenant_id", "recipe_sku"),
	}
}
