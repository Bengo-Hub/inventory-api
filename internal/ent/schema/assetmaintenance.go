package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetMaintenance tracks maintenance activities (ERP assets.AssetMaintenance).
type AssetMaintenance struct{ ent.Schema }

func (AssetMaintenance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.Enum("maintenance_type").Values("preventive", "corrective", "emergency", "predictive", "condition_based").Default("preventive"),
		field.Time("scheduled_date"),
		field.Time("completed_date").Optional().Nillable(),
		field.String("performed_by").Optional(),
		field.Float("cost").Default(0),
		field.Text("description").Optional(),
		field.Text("findings").Optional(),
		field.Text("recommendations").Optional(),
		field.Time("next_maintenance_date").Optional().Nillable(),
		field.Enum("status").Values("scheduled", "in_progress", "completed", "cancelled", "deferred").Default("scheduled"),
		field.Enum("priority").Values("low", "medium", "high", "critical").Default("medium"),
		field.Float("downtime_hours").Default(0),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetMaintenance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "status"),
	}
}
