package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetReservation tracks asset bookings (ERP assets.AssetReservation).
type AssetReservation struct{ ent.Schema }

func (AssetReservation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.UUID("reserved_by", uuid.UUID{}),
		field.Time("start_date"),
		field.Time("end_date"),
		field.Text("purpose").Optional(),
		field.Enum("status").Values("pending", "approved", "active", "completed", "cancelled").Default("pending"),
		field.UUID("approved_by", uuid.UUID{}).Optional().Nillable(),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetReservation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "status"),
	}
}
