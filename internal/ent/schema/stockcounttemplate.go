package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockCountTemplate is a reusable department count sheet — e.g. the kitchen's
// daily stock sheet chefs fill at shift close, or the barista counter list.
// Starting a stock take from a template pre-populates exactly the template's
// items (explicit item_ids plus every item in category_ids) with their current
// system on-hand, so "expected closing stock" comes from the system and the
// staff only type the physical count.
type StockCountTemplate struct {
	ent.Schema
}

// Fields of the StockCountTemplate.
func (StockCountTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").NotEmpty().Comment("Sheet name, e.g. Kitchen Daily Stock Sheet"),
		field.Text("description").Optional(),
		field.UUID("warehouse_id", uuid.UUID{}).Optional().Nillable().
			Comment("Default location this sheet counts; nil = chosen at start"),
		field.JSON("item_ids", []uuid.UUID{}).Optional().
			Comment("Explicit items on the sheet"),
		field.JSON("category_ids", []uuid.UUID{}).Optional().
			Comment("Whole categories on the sheet (resolved to items at start time)"),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the StockCountTemplate.
func (StockCountTemplate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
