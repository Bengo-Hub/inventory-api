package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Warehouse holds the schema definition for warehouses/locations.
type Warehouse struct {
	ent.Schema
}

// Fields of the Warehouse.
func (Warehouse) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").
			NotEmpty(),
		field.String("code").
			NotEmpty().
			Comment("Short code for the warehouse, unique per tenant"),
		field.Text("address").
			Optional(),
		field.Bool("is_default").
			Default(false).
			Comment("Default warehouse for the tenant"),
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

// Edges of the Warehouse.
func (Warehouse) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("warehouses").
			Unique().
			Required().
			Field("tenant_id"),
		edge.To("balances", InventoryBalance.Type),
		edge.To("reservations", Reservation.Type),
	}
}

// Indexes of the Warehouse.
func (Warehouse) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "is_default"),
		index.Fields("tenant_id", "is_active"),
	}
}
