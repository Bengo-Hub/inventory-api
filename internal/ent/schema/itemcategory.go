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
		field.String("name").
			NotEmpty(),
		field.String("code").
			MaxLen(10).
			Optional().
			Comment("Short code for SKU generation (e.g. BEV, PST)"),
		field.Text("description").
			Optional(),
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
	}
}
