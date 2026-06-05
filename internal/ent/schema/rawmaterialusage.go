package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RawMaterialUsage is an audit-trail row recording every movement of a raw
// material in/out of production (migrated from ERP manufacturing.RawMaterialUsage).
// Written on batch start (production), cancel (return), and QC fail/scrap (wastage).
type RawMaterialUsage struct {
	ent.Schema
}

func (RawMaterialUsage) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Comment("Owning tenant"),
		field.UUID("production_batch_id", uuid.UUID{}).Optional().Nillable().Comment("Originating batch, if any"),
		field.UUID("finished_item_id", uuid.UUID{}).Optional().Nillable().Comment("Finished good produced (recipe item)"),
		field.UUID("raw_item_id", uuid.UUID{}).Comment("Raw material item consumed/returned"),
		field.String("raw_sku").Optional().Comment("Raw material SKU snapshot"),
		field.Float("quantity").Default(0).Comment("Quantity moved (always positive; direction implied by transaction_type)"),
		field.Float("cost").Default(0).Comment("Valued cost of the movement"),
		field.Enum("transaction_type").
			Values("production", "testing", "wastage", "return", "adjustment").
			Default("production"),
		field.Text("notes").Optional(),
		field.Time("occurred_at").Default(time.Now).Immutable(),
	}
}

func (RawMaterialUsage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "production_batch_id"),
		index.Fields("tenant_id", "raw_item_id"),
		index.Fields("tenant_id", "transaction_type"),
	}
}
