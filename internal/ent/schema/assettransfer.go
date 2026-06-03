package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetTransfer tracks asset moves between locations/users (ERP assets.AssetTransfer).
type AssetTransfer struct{ ent.Schema }

func (AssetTransfer) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.String("from_location").Optional(),
		field.String("to_location").Optional(),
		field.UUID("from_user", uuid.UUID{}).Optional().Nillable(),
		field.UUID("to_user", uuid.UUID{}).Optional().Nillable(),
		field.Time("transfer_date").Default(time.Now),
		field.Time("scheduled_date").Optional().Nillable(),
		field.Enum("status").Values("pending", "approved", "in_transit", "completed", "cancelled").Default("pending"),
		field.Text("reason").Optional(),
		field.UUID("transferred_by", uuid.UUID{}).Optional().Nillable(),
		field.UUID("approved_by", uuid.UUID{}).Optional().Nillable(),
		field.Text("notes").Optional(),
		field.String("tracking_number").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetTransfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "status"),
	}
}
