package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetAudit tracks physical verification of assets (ERP assets.AssetAudit).
type AssetAudit struct{ ent.Schema }

func (AssetAudit) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.Time("audit_date"),
		field.UUID("auditor_id", uuid.UUID{}).Optional().Nillable(),
		field.Enum("status").Values("planned", "in_progress", "completed", "cancelled").Default("planned"),
		field.String("location_verified").Optional(),
		field.String("condition_verified").Optional(),
		field.UUID("custodian_verified", uuid.UUID{}).Optional().Nillable(),
		field.Text("discrepancies").Optional(),
		field.Text("recommendations").Optional(),
		field.Time("next_audit_date").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetAudit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "status"),
	}
}
