package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// GoodsReceiptLine is a received line on a GoodsReceipt, mapped back to the
// originating PurchaseOrderLine, capturing accepted vs rejected quantities.
type GoodsReceiptLine struct{ ent.Schema }

func (GoodsReceiptLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("goods_receipt_id", uuid.UUID{}),
		field.UUID("purchase_order_line_id", uuid.UUID{}).Optional().Nillable().Comment("FK to PurchaseOrderLine"),
		field.UUID("item_id", uuid.UUID{}).Comment("FK to Item"),
		field.Float("quantity_received").Default(0),
		field.Float("quantity_accepted").Default(0),
		field.Float("quantity_rejected").Default(0),
		field.Float("unit_cost").Default(0),
		field.Text("rejection_reason").Optional(),
		field.JSON("serials", []string{}).Optional().Comment("Serial numbers received on this line (serial-tracked items): one per unit accepted"),
		field.String("lot_number").Optional().Comment("Batch/lot number for lot-tracked items received on this line"),
		field.Time("expiry_date").Optional().Nillable().Comment("Lot expiry; seeded from item.shelf_life_days when blank for perishables"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (GoodsReceiptLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("goods_receipt", GoodsReceipt.Type).
			Ref("lines").
			Field("goods_receipt_id").
			Unique().
			Required(),
	}
}

func (GoodsReceiptLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "goods_receipt_id"),
		index.Fields("item_id"),
	}
}
