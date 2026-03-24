package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Bundle holds the schema for pre-packaged item kits/bundles.
// Unlike Recipes (assembled from ingredients), Bundles are groups of existing items
// sold as a single unit — e.g., "Back to School Kit" (notebook + pen + ruler).
type Bundle struct {
	ent.Schema
}

// Fields of the Bundle.
func (Bundle) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("The bundle item SKU (type=GOODS with is_bundle=true)"),
		field.String("name").
			NotEmpty(),
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

// Edges of the Bundle.
func (Bundle) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("bundle").
			Field("item_id").
			Unique().
			Required(),
		edge.To("components", BundleComponent.Type),
	}
}

// Indexes of the Bundle.
func (Bundle) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
