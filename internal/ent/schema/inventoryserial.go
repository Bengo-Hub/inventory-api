package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// InventorySerial is a per-unit serial-number record for serial-tracked items
// (electronics, equipment). Each physical unit of an item gets one row, captured
// at goods-receipt and transitioned to "sold" when the POS sells that specific unit.
// This complements the aggregate InventoryBalance with unit-level traceability
// (warranty, returns, audit).
type InventorySerial struct{ ent.Schema }

func (InventorySerial) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).Comment("FK to Item"),
		field.UUID("warehouse_id", uuid.UUID{}).Optional().Nillable().Comment("Current location; nil once sold"),
		field.String("serial_number").NotEmpty().Comment("Unique per (tenant, item)"),
		field.Enum("status").
			Values("available", "reserved", "sold", "returned", "defective").
			Default("available"),
		field.Time("received_at").Default(time.Now).Comment("When the unit entered stock"),
		field.UUID("goods_receipt_line_id", uuid.UUID{}).Optional().Nillable().Comment("FK to GoodsReceiptLine that received this unit"),
		field.Time("sold_at").Optional().Nillable().Comment("When the unit was sold"),
		field.String("pos_order_line_id").Optional().Comment("pos-api order line id that sold this unit"),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (InventorySerial) Indexes() []ent.Index {
	return []ent.Index{
		// One serial can exist only once per item within a tenant.
		index.Fields("tenant_id", "item_id", "serial_number").Unique(),
		index.Fields("tenant_id", "item_id", "status"),
		index.Fields("tenant_id", "status"),
	}
}
