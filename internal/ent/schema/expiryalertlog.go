package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ExpiryAlertLog records that a lot has already been alerted at a given severity tier, so the
// expiry scheduler never re-alerts the same lot/tier combination — a lot outstanding for weeks
// would otherwise re-fire an event every hourly tick.
type ExpiryAlertLog struct{ ent.Schema }

func (ExpiryAlertLog) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("lot_id", uuid.UUID{}),
		field.Enum("tier").
			Values("warning", "critical").
			Comment("warning = within TenantInventoryConfig.expiry_warning_days; critical = within 7 days"),
		field.Time("alerted_at").Default(time.Now).Immutable(),
	}
}

func (ExpiryAlertLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "lot_id", "tier").Unique(),
	}
}
