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
		field.UUID("unit_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK → Unit (preferred over unit_of_measure string for UI)"),
		field.Float("waste_percent").
			Default(0).
			Optional().
			Comment("Shrinkage/waste factor in %; effective qty = quantity * (1 + waste_percent/100)"),
		field.UUID("sub_recipe_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK → Recipe used as a prep/sub-recipe instead of a raw item"),
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
		edge.From("unit", Unit.Type).
			Ref("recipe_ingredients").
			Field("unit_id").
			Unique(),
		edge.From("sub_recipe", Recipe.Type).
			Ref("used_as_ingredient").
			Field("sub_recipe_id").
			Unique(),
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
