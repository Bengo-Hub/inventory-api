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
	}
}

// Indexes of the Recipe.
func (Recipe) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
