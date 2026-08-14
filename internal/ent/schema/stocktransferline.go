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
		field.Float("quantity").
			Default(0).
			Comment("Quantity to transfer (fractional-capable)"),
		// received_quantity is nil until the transfer is received. Defaults to the full drafted
		// Quantity when the receiver doesn't specify otherwise (zero behavior change for the
		// common full-receipt case) — set lower when what actually arrived at the destination
		// falls short of what was shipped (breakage/loss in transit, a miscount at dispatch).
		field.Float("received_quantity").
			Optional().
			Nillable().
			Comment("Quantity actually credited to the destination warehouse at Receive; nil until received"),
		// variance_reason classifies a shortfall (received_quantity < quantity) using the SAME
		// vocabulary StockAdjustment/StockCountLine already use, so transfer-shortage reporting
		// reads consistently with every other stock-variance surface in the app.
		field.String("variance_reason").
			Optional().
			Comment("Classification when received_quantity < quantity: damaged/expired/shrinkage/found/correction/count_variance/other"),
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
