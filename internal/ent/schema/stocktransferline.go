package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockTransferLine holds the schema for individual items on a stock transfer.
type StockTransferLine struct {
	ent.Schema
}

// Fields of the StockTransferLine.
func (StockTransferLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("transfer_id", uuid.UUID{}).
			Comment("FK to StockTransfer"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item"),
		field.UUID("variant_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to ItemVariant if variant-specific"),
		field.UUID("lot_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to InventoryLot for lot-tracked items"),
		field.Int("quantity").
			Default(0).
			Comment("Quantity to transfer"),
	}
}

// Edges of the StockTransferLine.
func (StockTransferLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transfer", StockTransfer.Type).
			Ref("lines").
			Field("transfer_id").
			Unique().
			Required(),
	}
}

// Indexes of the StockTransferLine.
func (StockTransferLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("transfer_id", "item_id"),
	}
}
