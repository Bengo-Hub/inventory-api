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
		field.Float("latitude").
			Optional().
			Nillable().
			Comment("GPS latitude for logistics routing"),
		field.Float("longitude").
			Optional().
			Nillable().
			Comment("GPS longitude for logistics routing"),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Outlet this warehouse serves as default stock source; nil = shared/HQ"),
		field.String("use_case").
			Optional().
			Comment("Mirror of the outlet's use_case (hospitality|retail|pharmacy|...), synced from auth.outlet events; drives backend per-use-case route gating"),
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
		edge.To("lots", InventoryLot.Type),
		edge.To("purchase_orders", PurchaseOrder.Type),
	}
}

// Indexes of the Warehouse.
func (Warehouse) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "is_default"),
		index.Fields("tenant_id", "is_active"),
		index.Fields("tenant_id", "outlet_id"),
	}
}
