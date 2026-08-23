package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockTransfer holds the schema for inter-warehouse stock transfers.
// Supports outlet-to-outlet transfers, warehouse consolidation, and redistribution.
type StockTransfer struct {
	ent.Schema
}

// Fields of the StockTransfer.
func (StockTransfer) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("source_warehouse_id", uuid.UUID{}).
			Comment("FK to source Warehouse"),
		field.UUID("destination_warehouse_id", uuid.UUID{}).
			Comment("FK to destination Warehouse"),
		field.String("transfer_number").
			NotEmpty().
			Comment("Unique transfer reference per tenant"),
		field.Enum("status").
			Values("draft", "in_transit", "received", "cancelled").
			Default("draft"),
		field.UUID("initiated_by", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("User who initiated the transfer"),
		// origin distinguishes a normal user-initiated transfer (New Transfer dialog) from one
		// auto-recorded, already-"received", after the fact by another feature that legitimately
		// moves stock between two warehouses outside the draft/ship/receive workflow (e.g. a bulk
		// stock adjustment whose lines specify a destination warehouse) — so every warehouse-to-
		// warehouse move gets a transfer_number and shows up in the Transfers list/reporting
		// regardless of entry point, without forcing an unrelated feature through an approval gate
		// meant for a chosen, still-in-flight shipment. See transfers.Service.RecordCompletedTransfer.
		field.String("origin").
			Default("manual").
			Comment("manual (New Transfer dialog) or an automated source, e.g. bulk_adjust"),
		field.String("reference_no").
			Optional().
			Comment("External/business reference number for the transfer (e.g. waybill, dispatch note)"),
		field.Float("shipping_charges").
			Default(0).
			Comment("Freight/shipping cost for moving the stock; posted to treasury as an expense on completion"),
		field.String("carrier").
			Optional().
			Comment("Carrier/courier handling the transfer"),
		field.Text("freight_notes").
			Optional().
			Comment("Notes about the shipment/freight (route, handling, seal references)"),
		field.Text("notes").
			Optional(),
		// transfer_date is a user-editable override of which calendar day this transfer is
		// recorded/reported under, distinct from the immutable created_at (server-entry
		// timestamp) — lets staff backdate a transfer entered late (stock physically moved days
		// ago, only logged today) or schedule one ahead for a planned future shipment. Unlike
		// pos-api's POSOrder.business_date (backdate-only, capped at "not in the future"), a
		// transfer may legitimately go either direction — client-requested "back date au tupeleke
		// date mbele" (back-date or push the date forward). Nil = display/report under created_at,
		// identical to every row that existed before this field. See transfers.EffectiveTransferDate.
		field.Time("transfer_date").
			Optional().
			Nillable(),
		field.Time("shipped_at").
			Optional().
			Nillable(),
		field.Time("received_at").
			Optional().
			Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the StockTransfer.
func (StockTransfer) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("lines", StockTransferLine.Type),
	}
}

// Indexes of the StockTransfer.
func (StockTransfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "transfer_number").Unique(),
		index.Fields("tenant_id", "status"),
	}
}
