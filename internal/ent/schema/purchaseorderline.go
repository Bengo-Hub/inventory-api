package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PurchaseOrderLine holds the schema for individual items on a purchase order.
type PurchaseOrderLine struct {
	ent.Schema
}

// Fields of the PurchaseOrderLine.
func (PurchaseOrderLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("po_id", uuid.UUID{}).
			Comment("FK to PurchaseOrder"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item"),
		field.UUID("variant_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to ItemVariant if variant-specific"),
		field.Int("quantity_ordered").
			Default(0),
		field.Int("quantity_received").
			Default(0),
		field.Float("unit_price").
			Default(0),
		field.Float("total_price").
			Default(0).
			Comment("quantity_ordered * unit_price"),
	}
}

// Edges of the PurchaseOrderLine.
func (PurchaseOrderLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("purchase_order", PurchaseOrder.Type).
			Ref("lines").
			Field("po_id").
			Unique().
			Required(),
	}
}

// Indexes of the PurchaseOrderLine.
func (PurchaseOrderLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("po_id", "item_id").Unique(),
	}
}
