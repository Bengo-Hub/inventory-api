package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockAdjustment records an audit trail for every stock level change.
type StockAdjustment struct {
	ent.Schema
}

// Fields of the StockAdjustment.
func (StockAdjustment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).Comment("FK to Item"),
		field.UUID("warehouse_id", uuid.UUID{}).Comment("FK to Warehouse"),
		field.Float("quantity_before").Comment("Stock level before adjustment"),
		field.Float("quantity_change").Comment("Delta: positive=increase, negative=decrease"),
		field.Float("quantity_after").Comment("Stock level after adjustment"),
		field.Enum("reason").
			Values("damaged", "expired", "shrinkage", "found", "correction",
				"transfer_in", "transfer_out", "return", "initial_count", "other").
			Comment("Reason for adjustment"),
		field.String("reference").Optional().Comment("External reference (e.g. PO number, transfer ID)"),
		field.Text("notes").Optional().Comment("Free-text notes"),
		field.UUID("adjusted_by", uuid.UUID{}).Comment("User who made the adjustment"),
		field.Time("adjusted_at").Default(time.Now),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Indexes of the StockAdjustment.
func (StockAdjustment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id"),
		index.Fields("tenant_id", "warehouse_id"),
		index.Fields("tenant_id", "reason"),
		index.Fields("adjusted_at"),
	}
}
