package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// StockClearance is a temporary, item-level markdown tied to old/aging stock (2026-09-06
// pricing/tiering plan, Phase 2 — see .claude/plans/pricing-multibranch-batch-flashsale-audit-
// and-plan-2026-09-06.md §3.2). Deliberately item-scoped, not lot-scoped: nothing in the
// POS/ordering checkout path is batch-aware at sale time (inventory-api alone decides which lot
// FIFO/LIFO/FEFO draws down, after the fact), so a markdown that depended on "sell THIS specific
// physical unit at a different price" would need a much bigger lot-reservation redesign than the
// real-world need calls for. Instead: mark the item's price down while its old stock (any lot
// received before reference_before) is still around, and auto-revert — the SAME lazy-resolve-on-
// read idiom PendingPriceChange already uses in production, just for a temporary discount instead
// of a permanent price replacement, and reverting instead of applying once the trigger condition
// is met.
type StockClearance struct {
	ent.Schema
}

func (StockClearance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}),
		field.Float("markdown_price").
			Comment("Temporary reduced price while active; normal tier/recipe resolution resumes once inactive"),
		field.Time("reference_before").
			Comment("Lots received before this time are the 'old stock' being cleared; the clearance auto-depletes once none remain active with quantity>0"),
		field.Time("starts_at").
			Default(time.Now).
			Comment("When the markdown became/becomes visible to customers"),
		field.Time("ends_at").
			Optional().
			Nillable().
			Comment("Optional fixed end time; nil = purely depletion-driven (no time limit, reverts only when old stock runs out)"),
		field.Enum("status").
			Values("active", "expired", "depleted", "cancelled").
			Default("active"),
		field.Time("ended_at").
			Optional().
			Nillable().
			Comment("When status left 'active' and why (ends_at reached vs. old stock depleted vs. manually cancelled)"),
		field.UUID("created_by", uuid.UUID{}).
			Optional().
			Nillable(),
		field.String("notes").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

func (StockClearance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id").
			StorageKey("stockclearance_tenant_item_lookup"),
		// At most one ACTIVE clearance per item at a time — mirrors ItemPricing's
		// itempricing_active_no_outlet partial-unique-index pattern (see pricing_enrich.go's
		// upsertItemTierPrice comments for the production race this style of guard closes).
		// Explicit StorageKey: without it this collides on the same auto-generated name as the
		// plain lookup index above (identical field list), which silently drops ITS Where
		// annotation instead of failing loudly — caught by inspecting the generated schema.go.
		index.Fields("tenant_id", "item_id").
			Unique().
			StorageKey("stockclearance_active_unique").
			Annotations(entsql.IndexWhere("status = 'active'")),
	}
}
