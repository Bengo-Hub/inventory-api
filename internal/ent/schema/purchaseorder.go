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
			Optional().
			Nillable().
			Comment("FK to Supplier — optional for draft POs (e.g. procure-to-order) until a supplier is assigned"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to Warehouse — destination for received goods; optional until resolved"),
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
		field.UUID("requisition_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Source requisition this PO was converted from (traceability)"),
		// project_id flows from the source requisition so the received cost is attributed to the
		// project in treasury (carried on the purchase_order.received event).
		field.UUID("project_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("projects-service project id (inherited from the source requisition)"),
		field.UUID("rfq_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Source RFQ this PO was awarded from (traceability)"),
		// Procure-to-order: link back to the treasury sales quotation that triggered this
		// draft PO (consumed from quotation.accepted). Used for idempotency so a redelivered
		// event never creates a duplicate PO for the same quotation.
		field.UUID("quotation_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Source treasury sales quotation this procure-to-order PO was auto-created from (traceability + idempotency)"),
		field.String("quotation_number").
			Optional().
			Comment("Human-readable number of the source sales quotation (procure-to-order)"),
		field.Int("pay_term_days").
			Optional().
			Nillable().
			Comment("Supplier credit term in days — treasury computes the AP bill due date as receipt + pay_term_days"),
		field.Float("additional_shipping_charges").
			Default(0).
			Comment("Freight/shipping added on the PO; posted as a treasury expense on receipt"),
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
			Unique(),
		edge.From("warehouse", Warehouse.Type).
			Ref("purchase_orders").
			Field("warehouse_id").
			Unique(),
		edge.To("lines", PurchaseOrderLine.Type),
	}
}

// Indexes of the PurchaseOrder.
func (PurchaseOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "po_number").Unique(),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "supplier_id"),
		// Idempotency lookup for procure-to-order: at most one PO per quotation per tenant.
		index.Fields("tenant_id", "quotation_id"),
	}
}
