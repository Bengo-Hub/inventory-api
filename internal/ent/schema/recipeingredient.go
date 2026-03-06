package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RecipeIngredient links a Recipe to one raw inventory Item with quantity info.
type RecipeIngredient struct {
	ent.Schema
}

// Fields of the RecipeIngredient.
func (RecipeIngredient) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("recipe_id", uuid.UUID{}).
			Comment("FK → Recipe"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK → Item (raw ingredient)"),
		field.String("item_sku").
			NotEmpty().
			MaxLen(100).
			Comment("Denormalised SKU for quick lookup without join"),
		field.Float("quantity").
			Positive().
			Comment("Amount of the ingredient consumed per output_qty portions"),
		field.String("unit_of_measure").
			Default("PIECE").
			MaxLen(20).
			Comment("PIECE, KG, LITRE, GRAM, ML — must match item.unit_of_measure"),
		field.String("notes").
			Optional().
			MaxLen(255).
			Comment("Optional prep notes, e.g. 'sliced', 'diced'"),
		field.Int("display_order").
			Default(0).
			Comment("Sort order within the recipe"),
	}
}

// Edges of the RecipeIngredient.
func (RecipeIngredient) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("recipe", Recipe.Type).
			Ref("ingredients").
			Field("recipe_id").
			Unique().
			Required(),
		edge.From("item", Item.Type).
			Ref("recipe_ingredients").
			Field("item_id").
			Unique().
			Required(),
	}
}

// Indexes of the RecipeIngredient.
func (RecipeIngredient) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("recipe_id", "item_id").Unique(),
		index.Fields("recipe_id"),
		index.Fields("item_sku"),
	}
}
