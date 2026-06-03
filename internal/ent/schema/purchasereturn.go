package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PurchaseReturn is a supplier RMA against a purchase (ERP PurchaseReturn).
type PurchaseReturn struct{ ent.Schema }

func (PurchaseReturn) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("return_number").Optional(),
		field.UUID("purchase_order_id", uuid.UUID{}).Optional().Nillable().Comment("FK to originating PurchaseOrder"),
		field.UUID("supplier_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("added_by", uuid.UUID{}).Optional().Nillable().Comment("Auth user"),
		field.Text("reason").Optional(),
		field.Float("return_amount").Default(0),
		field.Float("return_amount_due").Default(0),
		field.Enum("payment_status").Values("pending", "due", "partial", "paid").Default("pending"),
		field.Time("date_returned").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (PurchaseReturn) Edges() []ent.Edge {
	return []ent.Edge{edge.To("lines", PurchaseReturnLine.Type)}
}

func (PurchaseReturn) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "purchase_order_id"),
		index.Fields("tenant_id", "payment_status"),
	}
}
