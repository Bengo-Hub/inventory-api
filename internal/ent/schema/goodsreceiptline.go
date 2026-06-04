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
		field.Int("quantity_received").Default(0),
		field.Int("quantity_accepted").Default(0),
		field.Int("quantity_rejected").Default(0),
		field.Float("unit_cost").Default(0),
		field.Text("rejection_reason").Optional(),
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
