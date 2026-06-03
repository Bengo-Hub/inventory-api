package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetDisposal tracks asset disposals (ERP assets.AssetDisposal).
type AssetDisposal struct{ ent.Schema }

func (AssetDisposal) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.Time("disposal_date"),
		field.Enum("disposal_method").Values("sold", "scrapped", "donated", "stolen", "returned", "recycled", "destroyed").Default("sold"),
		field.Float("disposal_value").Default(0),
		field.Text("reason").Optional(),
		field.UUID("approved_by", uuid.UUID{}).Optional().Nillable(),
		field.Enum("status").Values("pending", "approved", "rejected", "completed", "cancelled").Default("pending"),
		field.Text("notes").Optional(),
		field.String("disposal_certificate").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetDisposal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "status"),
	}
}
