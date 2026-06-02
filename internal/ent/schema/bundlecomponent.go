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
		// Hospitality package semantics — classify what role this component plays in a package.
		field.Enum("component_kind").
			Values("ITEM", "MEAL_PERIOD", "AV_EQUIPMENT", "STATIONERY", "CONSUMABLE", "FACILITY", "SERVICE_SESSION").
			Default("ITEM").
			Comment("Role of this component within a (conference/event) package"),
		field.Enum("meal_period").
			Values("breakfast", "am_break", "lunch", "pm_break", "dinner").
			Optional().
			Nillable().
			Comment("Meal period for MEAL_PERIOD components — drives delegate meal-card generation in pos-api"),
		field.Bool("is_metered").
			Default(false).
			Comment("True if consumption is metered/charged on actuals rather than included flat"),
		field.String("unit").
			Optional().
			Comment("Human-readable unit for the component, e.g. '2x500ml water'"),
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
