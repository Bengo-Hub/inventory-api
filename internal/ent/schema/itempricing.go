package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemPricing holds the schema for per-tier pricing of an item.
type ItemPricing struct{ ent.Schema }

// Fields of the ItemPricing.
func (ItemPricing) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).Comment("References Item"),
		field.UUID("pricing_tier_id", uuid.UUID{}),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Outlet-level rate override (nil = applies to all outlets). Used for hospitality per-outlet room/facility rates"),
		field.Enum("tier_basis").
			Values("default", "nightly", "per_session", "per_delegate_per_day", "peak", "off_peak").
			Default("default").
			Comment("Pricing basis/season for hospitality rate tiers"),
		field.Float("price").Default(0).Comment("Price for this tier"),
		field.String("currency").Default("KES"),
		field.Time("effective_from").Default(time.Now),
		field.Time("effective_to").Optional().Nillable(),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Indexes of the ItemPricing.
func (ItemPricing) Indexes() []ent.Index {
	return []ent.Index{
		// Includes effective_from (not just tenant/item/tier/outlet) so a real price HISTORY can
		// accumulate: closing out the old row (is_active=false, effective_to=now) and inserting a
		// new one at a fresh effective_from is how a selling-price change is recorded from here on,
		// rather than overwriting the row in place and losing what it used to be. A superset of the
		// previous (tenant_id,item_id,pricing_tier_id,outlet_id) index, so it can only be a weaker
		// constraint than before — safe against any data that already satisfied it.
		index.Fields("tenant_id", "item_id", "pricing_tier_id", "outlet_id", "effective_from").Unique(),
		index.Fields("item_id"),
		index.Fields("outlet_id"),
		index.Fields("pricing_tier_id"),
		index.Fields("is_active"),
		index.Fields("tenant_id", "item_id", "pricing_tier_id", "outlet_id", "is_active"),
		// At most one ACTIVE row per (tenant, item, tier[, outlet]) — upsertItemTierPrice's
		// deactivate-old/insert-new sequence (pricing_enrich.go) is a check-then-act race with no DB
		// guard otherwise: two overlapping price-change requests for the same item can both insert a
		// new active row, and defaultTierPrices' resolution query (no ORDER BY) then picks between
		// the duplicates arbitrarily — a price edit that silently "doesn't stick". A deactivated
		// (is_active=false) historical row is excluded from these indexes, so history keeps
		// accumulating exactly as the effective_from index above intends; split on outlet_id IS
		// NULL/NOT NULL the same way POSCatalogOverride's dedupe guard is, since a plain unique index
		// never treats two NULLs as equal. See migration
		// 20260812063000_item_pricing_active_unique_guard.sql.
		index.Fields("tenant_id", "item_id", "pricing_tier_id").
			Unique().
			StorageKey("itempricing_active_no_outlet").
			Annotations(entsql.IndexWhere("is_active AND outlet_id IS NULL")),
		index.Fields("tenant_id", "item_id", "pricing_tier_id", "outlet_id").
			Unique().
			StorageKey("itempricing_active_outlet").
			Annotations(entsql.IndexWhere("is_active AND outlet_id IS NOT NULL")),
	}
}
