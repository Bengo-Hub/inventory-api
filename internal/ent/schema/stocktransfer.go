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
		field.Text("notes").
			Optional(),
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
