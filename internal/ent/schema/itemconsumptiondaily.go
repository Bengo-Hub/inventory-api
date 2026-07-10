package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemConsumptionDaily is a day-grain rollup of ConsumptionLine, incremented transactionally
// in the same write as each ConsumptionLine insert. Without it, a "this quarter" ingredient
// utilization query means scanning every sale line for 90 days on every page load — the
// rollup keeps range queries fast while staying real-time (updated synchronously on each
// sale, not by a nightly batch).
//
// recipe_id is NOT nullable here (unlike ConsumptionLine): it defaults to uuid.Nil for a
// directly-sold item with no BOM, specifically so the (tenant, warehouse, item, recipe,
// bucket_date) unique index dedupes correctly — Postgres treats every NULL in a unique
// index as distinct, which would silently defeat the upsert for the no-recipe case.
type ItemConsumptionDaily struct {
	ent.Schema
}

// Fields of the ItemConsumptionDaily.
func (ItemConsumptionDaily) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("warehouse_id", uuid.UUID{}),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("The ingredient/raw item this bucket totals"),
		field.String("item_sku").
			Optional(),
		field.UUID("recipe_id", uuid.UUID{}).
			Comment("uuid.Nil when the item was sold directly with no BOM"),
		field.String("recipe_sku").
			Optional(),
		field.Time("bucket_date").
			Comment("Truncated to UTC midnight — the day this bucket totals"),
		field.Float("quantity").
			Default(0),
		field.Float("total_cost").
			Default(0),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Indexes of the ItemConsumptionDaily.
func (ItemConsumptionDaily) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "warehouse_id", "item_id", "recipe_id", "bucket_date").Unique(),
		index.Fields("tenant_id", "item_id", "bucket_date"),
	}
}
