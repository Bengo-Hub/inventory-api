package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockLevelEvent persists the low/out/restocked transitions that checkAndPublishLowStock
// already computes on every consumption but previously only published to the outbox. Having
// a row (not just a fired-and-forgotten event) lets reports phase-band a consumption trend
// into "above reorder level" / "below reorder level" / "stocked out" windows.
type StockLevelEvent struct {
	ent.Schema
}

// Fields of the StockLevelEvent.
func (StockLevelEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}),
		field.UUID("warehouse_id", uuid.UUID{}),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.Enum("event_type").
			Values("low", "out", "restocked"),
		field.Float("on_hand_at_event").
			Default(0),
		field.Float("reorder_level_at_event").
			Default(0),
		field.Time("occurred_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the StockLevelEvent.
func (StockLevelEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id", "warehouse_id", "occurred_at"),
	}
}
