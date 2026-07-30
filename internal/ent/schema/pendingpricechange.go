package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PendingPriceChange is a queued selling-price change requested with price_scope=new_stock_only
// on a goods receipt: "raise/lower the price, but let stock that was already on hand keep
// selling at what it's selling at now, until it's gone." It requires no batch-priced checkout —
// promotion is lazy (checked whenever a price is resolved, see items.Service.
// PromotePendingPriceChanges): once every cost layer that existed before this receipt
// (received_at < trigger_before) has depleted, tenant-wide, the queued price is applied for
// real and the row flips to "applied". The "applied" transition is a status=pending → applied
// compare-and-swap update, so a concurrent promotion attempt can only win once.
type PendingPriceChange struct{ ent.Schema }

func (PendingPriceChange) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}),
		field.Float("new_price"),
		field.String("currency").Default("KES"),
		field.Time("trigger_before").
			Comment("Layers with received_at before this time are 'old stock' — the change applies once none of them remain active with quantity > 0."),
		field.Text("reason").Optional(),
		field.UUID("created_by", uuid.UUID{}).Optional().Nillable(),
		field.UUID("goods_receipt_line_id", uuid.UUID{}).Optional().Nillable().
			Comment("Provenance + idempotency key: a retried GRN post must not queue a duplicate pending change for the same line."),
		field.Enum("status").
			Values("pending", "applied", "cancelled").
			Default("pending"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("applied_at").Optional().Nillable(),
	}
}

func (PendingPriceChange) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id", "status"),
		index.Fields("goods_receipt_line_id"),
	}
}
