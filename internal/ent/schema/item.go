package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Item holds the schema definition for inventory items.
type Item struct {
	ent.Schema
}

// Fields of the Item.
func (Item) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("sku").
			NotEmpty().
			Comment("Stock keeping unit, unique per tenant"),
		field.String("name").
			NotEmpty(),
		field.Text("description").
			Optional(),
		field.String("category").
			Optional().
			Comment("Item category for grouping"),
		field.Float("price").
			Default(0).
			Comment("Unit price in smallest currency unit"),
		field.String("unit_of_measure").
			Default("PIECE").
			Comment("PIECE, KG, LITRE, PORTION"),
		field.Bool("is_active").
			Default(true),
		field.String("image_url").
			Optional(),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Item.
func (Item) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("items").
			Unique().
			Required().
			Field("tenant_id"),
		edge.To("balances", InventoryBalance.Type),
		edge.To("recipe_ingredients", RecipeIngredient.Type),
	}
}

// Indexes of the Item.
func (Item) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").Unique(),
		index.Fields("tenant_id", "category"),
		index.Fields("tenant_id", "is_active"),
	}
}
