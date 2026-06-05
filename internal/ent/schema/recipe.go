package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Recipe holds the schema definition for a Bill of Materials (recipe) entity.
// A recipe links a menu item SKU to one or more raw ingredient items.
type Recipe struct {
	ent.Schema
}

// Fields of the Recipe.
func (Recipe) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("sku").
			NotEmpty().
			Comment("Matches MenuItem.sku in ordering-service — unique per tenant"),
		field.String("name").
			NotEmpty().
			MaxLen(255).
			Comment("Human-readable recipe name (mirrors menu item name)"),
		field.Float("output_qty").
			Default(1).
			Positive().
			Comment("How many portions this recipe produces (usually 1)"),
		field.String("unit_of_measure").
			Default("PORTION").
			MaxLen(20).
			Comment("Unit for output: PORTION, KG, LITRE"),
		field.Bool("is_active").
			Default(true),
		field.Enum("kind").
			Values("menu", "bom").
			Default("menu").
			Comment("menu = hospitality recipe; bom = manufacturing bill of materials. Drives use-case-aware UI framing"),
		field.Bool("requires_qc").
			Default(false).
			Comment("If true, completing a production batch for this recipe requires a passing QualityCheck"),
		// Recipe costing (Phase 7.1)
		field.Float("total_cost").
			Optional().
			Nillable().
			Comment("Sum of ingredient costs, auto-calculated from ingredient cost_prices"),
		field.Float("cost_per_portion").
			Optional().
			Nillable().
			Comment("total_cost / output_qty"),
		field.Float("target_margin_percent").
			Optional().
			Nillable().
			Comment("Desired profit margin percentage"),
		field.Float("suggested_price").
			Optional().
			Nillable().
			Comment("cost_per_portion / (1 - margin) — auto-calculated"),
		field.Float("selling_price").
			Optional().
			Nillable().
			Comment("The price this menu item sells for — user input, never overwritten. Used to compute food_cost_pct"),
		field.Float("food_cost_pct").
			Optional().
			Nillable().
			Comment("cost_per_portion / selling_price — auto-calculated; target range 0.28-0.35"),
		field.String("status").
			Optional().
			MaxLen(50).
			Comment("OK - healthy | OK - above target FC% | LOSS - cost >= price"),
		field.Int("prep_time_minutes").
			Optional().
			Nillable().
			Comment("Preparation time in minutes"),
		field.UUID("item_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK → the RECIPE-type Item this BOM produces"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}).
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Recipe.
func (Recipe) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("ingredients", RecipeIngredient.Type),
		edge.To("used_as_ingredient", RecipeIngredient.Type),
		edge.From("item", Item.Type).
			Ref("produced_by_recipe").
			Field("item_id").
			Unique(),
	}
}

// Indexes of the Recipe.
func (Recipe) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
