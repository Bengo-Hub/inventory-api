package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PricingTier holds the schema for multiple price levels per item.
// Examples: Retail, Wholesale, VIP, Staff.
type PricingTier struct{ ent.Schema }

// Fields of the PricingTier.
func (PricingTier) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").NotEmpty().Comment("Retail, Wholesale, VIP, Staff"),
		field.String("code").NotEmpty().Comment("RETAIL, WHOLESALE, VIP, STAFF"),
		field.String("description").Optional(),
		field.Bool("is_default").Default(false).Comment("Default tier for unlisted customers"),
		field.Bool("is_active").Default(true),
		field.Int("sort_order").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Indexes of the PricingTier.
func (PricingTier) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id"),
	}
}
