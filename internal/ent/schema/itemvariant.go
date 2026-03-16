package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemVariant holds the schema definition for item variations.
type ItemVariant struct {
	ent.Schema
}

// Fields of the ItemVariant.
func (ItemVariant) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("Parent item"),
		field.String("sku").
			NotEmpty().
			Comment("Variant specific SKU"),
		field.String("name").
			NotEmpty().
			Comment("e.g., Small, Blue, 500ml"),
		field.Float("price").
			Default(0).
			Comment("Variant specific price"),
		field.Bool("is_active").
			Default(true),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ItemVariant.
func (ItemVariant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("variants").
			Unique().
			Required().
			Field("item_id"),
	}
}

// Indexes of the ItemVariant.
func (ItemVariant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("item_id", "sku").Unique(),
	}
}
