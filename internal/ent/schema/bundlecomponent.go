package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// BundleComponent holds the schema for items included in a bundle.
type BundleComponent struct {
	ent.Schema
}

// Fields of the BundleComponent.
func (BundleComponent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("bundle_id", uuid.UUID{}).
			Comment("FK to Bundle"),
		field.UUID("component_item_id", uuid.UUID{}).
			Comment("FK to Item included in bundle"),
		field.Int("quantity").
			Default(1).
			Comment("Quantity of this component in the bundle"),
		field.Int("sort_order").
			Default(0),
	}
}

// Edges of the BundleComponent.
func (BundleComponent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("bundle", Bundle.Type).
			Ref("components").
			Field("bundle_id").
			Unique().
			Required(),
		edge.From("component_item", Item.Type).
			Ref("bundle_components").
			Field("component_item_id").
			Unique().
			Required(),
	}
}

// Indexes of the BundleComponent.
func (BundleComponent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("bundle_id", "component_item_id").Unique(),
	}
}
