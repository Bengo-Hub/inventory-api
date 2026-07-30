package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// InventoryLot holds the schema for batch/lot tracking.
// Supports pharmacy batch tracking, food lot expiry, perishable goods management.
type InventoryLot struct {
	ent.Schema
}

// Fields of the InventoryLot.
func (InventoryLot) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Comment("FK to Warehouse"),
		field.String("lot_number").
			NotEmpty().
			Comment("Batch/lot identifier from supplier or internal"),
		field.Time("expiry_date").
			Optional().
			Nillable().
			Comment("Expiration date for perishables/pharma"),
		field.Time("manufactured_date").
			Optional().
			Nillable().
			Comment("Manufacturing/production date"),
		field.Float("quantity").
			Default(0).
			Comment("Current quantity in this lot (fractional-capable)"),
		field.Enum("status").
			Values("active", "expired", "recalled", "depleted").
			Default("active"),
		field.Float("cost_price").
			Optional().
			Nillable().
			Comment("Cost per unit in this lot for FIFO/LIFO costing"),
		field.String("supplier_reference").
			Optional().
			Comment("Supplier PO or invoice reference"),
		field.Bool("is_cost_layer").
			Default(false).
			Comment("True for a layer auto-created at goods receipt for a non-lot-tracked item purely to preserve the cost actually paid. Hidden from the Lots admin UI, barcode labels, and expiry alerts — it carries no lot number a customer or regulator should ever see."),
		field.Time("received_at").
			Optional().
			Nillable().
			Comment("Actual receipt date, for FIFO/LIFO ordering. Distinct from created_at so a late-entered but earlier-dated receipt still orders correctly."),
		field.UUID("goods_receipt_line_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("The goods-receipt line that created this layer — provenance, and the idempotency key that prevents a retried GRN post from double-creating a layer."),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the InventoryLot.
func (InventoryLot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("lots").
			Field("item_id").
			Unique().
			Required(),
		edge.From("warehouse", Warehouse.Type).
			Ref("lots").
			Field("warehouse_id").
			Unique().
			Required(),
	}
}

// Indexes of the InventoryLot.
func (InventoryLot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id", "lot_number").Unique(),
		index.Fields("tenant_id", "expiry_date"),
		index.Fields("status"),
		index.Fields("tenant_id", "item_id"),
		index.Fields("tenant_id", "item_id", "warehouse_id", "status", "is_cost_layer"),
		index.Fields("goods_receipt_line_id"),
	}
}
