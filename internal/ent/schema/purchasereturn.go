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
		field.UUID("goods_receipt_id", uuid.UUID{}).Optional().Nillable().Comment("FK to the GoodsReceipt whose rejected lines auto-created this return (idempotency key: one auto return per GRN)"),
		field.UUID("supplier_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("warehouse_id", uuid.UUID{}).Optional().Nillable().Comment("Warehouse/location the returned goods leave from (the outlet's own warehouse by default). Nil on returns created before this field existed; approval then falls back to the linked GRN's warehouse, else the tenant default"),
		field.UUID("added_by", uuid.UUID{}).Optional().Nillable().Comment("Auth user"),
		field.Text("reason").Optional(),
		field.Float("return_amount").Default(0),
		field.Float("return_amount_due").Default(0),
		field.Enum("payment_status").Values("pending", "due", "partial", "paid").Default("pending"),
		field.Enum("destination").Values("supplier", "service").Default("supplier").
			Comment("Where the goods go. supplier = classic return to the supplier (credited by treasury). service = sent to a repair provider, service or care centre and expected back; no credit note, no eTIMS movement"),
		field.Enum("service_status").Values("out", "partial", "returned", "written_off").Optional().Nillable().
			Comment("Repair lifecycle of a destination=service return. Nil for supplier returns and for a service return not yet approved. out = at the provider, partial = some units back, returned = all back, written_off = nothing more is coming back"),
		field.Time("expected_back_date").Optional().Nillable().
			Comment("Calendar day (00:00 UTC) the provider is expected to return the goods; drives the overdue figure"),
		field.JSON("metadata", map[string]any{}).Optional().
			Comment("Service returns: service_provider, service_contact, fault, receive notes. Free text lives here to avoid schema churn"),
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
		index.Fields("tenant_id", "warehouse_id"),
		// Service tab and overdue figure: filter by destination + status, order by expected date.
		index.Fields("tenant_id", "destination", "service_status", "expected_back_date"),
		// Unique (not just indexed): enforces "one auto return per GRN" at the DB level, closing
		// the race in autoCreateReturnForRejected's Exist()-then-Create() check. Postgres unique
		// indexes treat every NULL as distinct, so manual returns (goods_receipt_id unset) are
		// unaffected — only two rows both pointing at the same non-null GRN collide.
		index.Fields("tenant_id", "goods_receipt_id").Unique(),
	}
}
