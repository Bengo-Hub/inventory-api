package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// InventoryBalance holds the schema for stock balances per item per warehouse.
type InventoryBalance struct {
	ent.Schema
}

// Fields of the InventoryBalance.
func (InventoryBalance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to items table"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Comment("FK to warehouses table"),
		field.Float("on_hand").
			Default(0).
			Comment("Total physical stock (fractional-capable)"),
		field.Float("available").
			Default(0).
			Comment("on_hand minus reserved (fractional-capable)"),
		field.Float("reserved").
			Default(0).
			Comment("Reserved for pending orders (fractional-capable)"),
		field.String("unit_of_measure").
			Default("PIECE"),
		field.Int("reorder_level").
			Default(1).
			Comment("Threshold below which a reorder notification is triggered"),
		field.Int("reorder_quantity").
			Default(0).
			Comment("Auto-reorder quantity when stock falls below reorder_level"),
		field.UUID("preferred_supplier_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Preferred supplier for auto-reorder PO generation"),
		field.Bool("auto_reorder_enabled").
			Default(false).
			Comment("Enable auto-creation of draft POs when below reorder_level"),
		field.UUID("location_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Optional sub-location within the warehouse (zone/aisle/rack/shelf/bin)"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the InventoryBalance.
func (InventoryBalance) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("balances").
			Field("item_id").
			Unique().
			Required(),
		edge.From("warehouse", Warehouse.Type).
			Ref("balances").
			Field("warehouse_id").
			Unique().
			Required(),
		edge.To("location", WarehouseLocation.Type).
			Field("location_id").
			Unique(),
	}
}

// Indexes of the InventoryBalance.
func (InventoryBalance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id", "warehouse_id").Unique(),
		index.Fields("tenant_id", "item_id"),
	}
}
