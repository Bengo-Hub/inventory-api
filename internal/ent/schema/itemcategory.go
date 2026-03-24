package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemCategory holds the schema definition for item categories.
// Supports hierarchical nesting via parent_id for industry-specific trees
// (e.g., Electronics > Phones > iPhones for retail, Food > Mains > Grilled for restaurants).
type ItemCategory struct {
	ent.Schema
}

// Fields of the ItemCategory.
func (ItemCategory) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("parent_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Parent category for hierarchy; nil = root category"),
		field.String("name").
			NotEmpty(),
		field.String("code").
			MaxLen(10).
			Optional().
			Comment("Short code for SKU generation (e.g. BEV, PST)"),
		field.String("slug").
			Optional().
			Comment("URL-safe slug for frontend routing"),
		field.String("icon").
			Optional().
			Comment("Emoji or icon class name for display"),
		field.Text("description").
			Optional(),
		field.Int("depth").
			Default(0).
			Comment("Nesting depth, 0 = root"),
		field.String("path").
			Optional().
			Comment("Materialized path: root-id/parent-id/self-id for efficient tree queries"),
		field.Int("sort_order").
			Default(0).
			Comment("Display ordering within the same parent"),
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

// Edges of the ItemCategory.
func (ItemCategory) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("items", Item.Type),
		edge.To("children", ItemCategory.Type).
			From("parent").
			Field("parent_id").
			Unique(),
		edge.To("custom_field_definitions", CustomFieldDefinition.Type),
		edge.From("tenant", Tenant.Type).
			Ref("item_categories").
			Unique().
			Required().
			Field("tenant_id"),
	}
}

// Indexes of the ItemCategory.
func (ItemCategory) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name"),
		index.Fields("tenant_id", "parent_id"),
		index.Fields("path"),
		index.Fields("tenant_id", "sort_order"),
	}
}
