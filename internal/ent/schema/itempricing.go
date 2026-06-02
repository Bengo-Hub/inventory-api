package schema

import (
	"time"

	"entgo.io/ent"
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
		index.Fields("tenant_id", "item_id", "pricing_tier_id", "outlet_id").Unique(),
		index.Fields("item_id"),
		index.Fields("outlet_id"),
		index.Fields("pricing_tier_id"),
		index.Fields("is_active"),
	}
}
