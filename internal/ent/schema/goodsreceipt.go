package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// GoodsReceipt is a Goods Receipt Note (GRN) recording the physical receipt of
// goods against a PurchaseOrder. Posting a GRN moves stock in and advances the
// PO status; it is the intermediate step enabling 3-way match (PO ↔ GRN ↔ invoice).
type GoodsReceipt struct{ ent.Schema }

func (GoodsReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("grn_number").NotEmpty().Comment("Sequence-numbered GRN reference"),
		field.UUID("purchase_order_id", uuid.UUID{}).Comment("FK to PurchaseOrder (loose ref)"),
		field.UUID("supplier_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("warehouse_id", uuid.UUID{}).Optional().Nillable().Comment("Destination warehouse for the stock-in"),
		field.UUID("received_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("received_date").Default(time.Now),
		field.Enum("status").Values("draft", "posted", "cancelled").Default("draft"),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (GoodsReceipt) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("lines", GoodsReceiptLine.Type),
	}
}

func (GoodsReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "grn_number").Unique(),
		index.Fields("tenant_id", "purchase_order_id"),
		index.Fields("tenant_id", "status"),
	}
}
