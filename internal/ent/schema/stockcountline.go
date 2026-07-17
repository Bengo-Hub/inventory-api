package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockCountLine is one item line within a StockCount: its system on-hand
// snapshot, the counted quantity, and the resulting variance.
type StockCountLine struct {
	ent.Schema
}

// Fields of the StockCountLine.
func (StockCountLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("stock_count_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}),
		field.String("sku").NotEmpty(),
		field.Float("system_qty").Default(0).Comment("System on-hand snapshot at count time"),
		field.Float("counted_qty").Optional().Nillable().Comment("Physically counted quantity"),
		field.Float("variance").Optional().Nillable().Comment("counted_qty - system_qty"),
		// Variance classification chosen during review (the client's "classified and
		// investigated as wastage, pilferage, …"). One of the StockAdjustment reasons
		// (damaged, expired, shrinkage, found, correction, count_variance, other);
		// empty = plain count_variance. Used as the posted adjustment's reason.
		field.String("reason").Optional().MaxLen(40).Comment("Variance classification — StockAdjustment reason used when posting (empty = count_variance)"),
		field.Bool("posted").Default(false).Comment("True once the variance adjustment was posted"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the StockCountLine.
func (StockCountLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "stock_count_id"),
		index.Fields("stock_count_id", "item_id").Unique(),
	}
}
