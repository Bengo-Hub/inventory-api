package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockBreakdown records a bulk-to-retail unit conversion (e.g. 1 crate -> 24 bottles, 1 sack -> kg).
// It decrements the parent SKU and increments the child SKU in one transaction, carrying cost
// parent->child so total inventory value is conserved. Distinct from BOM production (recipe
// transformation of inputs into a different finished good). See docs/sprints/sprint-procurement-breakdowns.md.
type StockBreakdown struct {
	ent.Schema
}

func (StockBreakdown) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("parent_item_id", uuid.UUID{}).Comment("FK to bulk Item"),
		field.String("parent_sku"),
		field.UUID("child_item_id", uuid.UUID{}).Comment("FK to retail-unit Item"),
		field.String("child_sku"),
		field.UUID("warehouse_id", uuid.UUID{}),
		field.Float("parent_quantity").Comment("bulk units consumed"),
		field.Float("child_quantity").Comment("retail units produced"),
		field.Float("conversion_factor").Comment("child units per parent unit"),
		field.Float("cost_allocated").Optional().Nillable().Comment("cost moved parent->child (value conservation)"),
		field.String("reference").Optional(),
		field.Text("notes").Optional(),
		field.UUID("created_by", uuid.UUID{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (StockBreakdown) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "parent_item_id"),
		index.Fields("tenant_id", "child_item_id"),
		index.Fields("created_at"),
	}
}
