package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Tenant holds the schema definition for the Tenant entity.
type Tenant struct {
	ent.Schema
}

// Fields of the Tenant.
func (Tenant) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("name").
			NotEmpty(),
		field.String("slug").
			NotEmpty().
			Unique(),
		field.String("status").
			Default("active"),
		field.String("contact_email").
			Optional(),
		field.String("contact_phone").
			Optional(),
		field.String("logo_url").
			Optional(),
		field.String("website").
			Optional(),
		field.String("country").
			Optional().
			Default("KE"),
		field.String("timezone").
			Optional().
			Default("Africa/Nairobi"),
		field.JSON("brand_colors", map[string]any{}).
			Optional(),
		field.String("org_size").
			Optional(),
		field.String("use_case").
			Optional(),
		field.String("subscription_plan").
			Optional(),
		field.String("subscription_status").
			Optional(),
		field.Time("subscription_expires_at").
			Optional().
			Nillable(),
		field.String("subscription_id").
			Optional(),
		field.JSON("tier_limits", map[string]any{}).
			Optional(),
		field.JSON("metadata", map[string]any{}).
			Optional().
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Tenant.
func (Tenant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("warehouses", Warehouse.Type),
		edge.To("items", Item.Type),
	}
}

// Indexes of the Tenant.
func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("slug").Unique(),
		index.Fields("status"),
	}
}
