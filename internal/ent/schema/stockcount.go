package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockCount is a cycle / physical inventory count session. Counted quantities
// are reconciled against system on-hand; an approved count posts variance
// StockAdjustments. Variance over a configured threshold requires approval —
// the loss-prevention control for stock shrinkage.
type StockCount struct {
	ent.Schema
}

// Fields of the StockCount.
func (StockCount) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("warehouse_id", uuid.UUID{}),
		field.String("reference").Optional().Comment("Human reference, e.g. CYCLE-2026-06"),
		field.Enum("status").
			Values("draft", "counting", "review", "approved", "cancelled").
			Default("draft"),
		field.UUID("counted_by", uuid.UUID{}).Optional().Nillable(),
		field.UUID("approved_by", uuid.UUID{}).Optional().Nillable(),
		field.Text("notes").Optional(),
		field.Time("approved_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the StockCount.
func (StockCount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "warehouse_id"),
		index.Fields("tenant_id", "status"),
	}
}
