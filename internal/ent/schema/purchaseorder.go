package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PurchaseOrder holds the schema for vendor purchase orders.
// Supports procurement workflows: draft → sent → partially_received → received.
type PurchaseOrder struct {
	ent.Schema
}

// Fields of the PurchaseOrder.
func (PurchaseOrder) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("supplier_id", uuid.UUID{}).
			Comment("FK to Supplier"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Comment("FK to Warehouse — destination for received goods"),
		field.String("po_number").
			NotEmpty().
			Comment("Unique PO number per tenant"),
		field.Enum("status").
			Values("draft", "sent", "partially_received", "received", "cancelled").
			Default("draft"),
		field.Time("expected_date").
			Optional().
			Nillable().
			Comment("Expected delivery date"),
		field.Float("total_amount").
			Default(0).
			Comment("Sum of line totals"),
		field.String("currency").
			Default("KES"),
		field.Text("notes").
			Optional(),
		field.UUID("created_by", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("User who created the PO"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the PurchaseOrder.
func (PurchaseOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("supplier", Supplier.Type).
			Ref("purchase_orders").
			Field("supplier_id").
			Unique().
			Required(),
		edge.From("warehouse", Warehouse.Type).
			Ref("purchase_orders").
			Field("warehouse_id").
			Unique().
			Required(),
		edge.To("lines", PurchaseOrderLine.Type),
	}
}

// Indexes of the PurchaseOrder.
func (PurchaseOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "po_number").Unique(),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "supplier_id"),
	}
}
