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
		field.UUID("unit_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to Unit — unit of measure this line was ordered in (e.g. kg, box); defaults to the item's purchase_unit"),
		field.Float("quantity_ordered").
			Default(0),
		field.Float("quantity_received").
			Default(0),
		field.Float("unit_price").
			Default(0),
		field.Float("total_price").
			Default(0).
			Comment("quantity_ordered * unit_price"),
		field.Float("rebate_percent").
			Default(0).
			Comment("supplier rebate %% accrued on the value received for this line"),
		// Selling-price adjustment decided at ORDER time and carried through to the goods
		// receipt as a default (still editable there) — so a buyer who already knows a price
		// change is coming doesn't have to re-decide it when the goods are actually received.
		// The decision is only APPLIED at goods-receipt-post time (see postGoodsReceiptCore /
		// SchedulePendingPriceChange), never here — nothing has been received yet at PO time.
		field.Float("new_selling_price").Optional().Nillable().
			Comment("Selling price to apply once this line is received, if the buyer chose to adjust it at order time."),
		field.String("price_scope").Optional().Default("all_stock").
			Comment("all_stock: apply new_selling_price immediately on receipt, everywhere. new_stock_only: queue it — old stock keeps its current price until every pre-receipt cost layer is depleted."),
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
