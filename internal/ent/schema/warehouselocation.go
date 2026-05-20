package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// WarehouseLocation holds the schema for hierarchical sub-locations within a warehouse.
// Hierarchy: Zone > Aisle > Rack > Shelf > Bin.
type WarehouseLocation struct{ ent.Schema }

// Fields of the WarehouseLocation.
func (WarehouseLocation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("warehouse_id", uuid.UUID{}),
		field.UUID("parent_id", uuid.UUID{}).Optional().Nillable().Comment("Parent location for hierarchy"),
		field.String("code").NotEmpty().Comment("Short code: Z1, A3, R2-S4, BIN-001"),
		field.String("name").NotEmpty().Comment("Human-readable: Zone 1, Aisle 3"),
		field.String("type").Default("bin").Comment("zone, aisle, rack, shelf, bin"),
		field.Int("depth").Default(0).Comment("0=zone, 1=aisle, 2=rack, 3=shelf, 4=bin"),
		field.String("path").Optional().Comment("Materialized path: /z1/a3/r2/s4/"),
		field.Bool("is_active").Default(true),
		field.Int("capacity_units").Optional().Nillable().Comment("Max storage capacity"),
		field.String("notes").Optional(),
	}
}

// Indexes of the WarehouseLocation.
func (WarehouseLocation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "warehouse_id", "code").Unique(),
		index.Fields("warehouse_id"),
		index.Fields("parent_id"),
		index.Fields("type"),
	}
}
