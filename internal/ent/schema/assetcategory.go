package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetCategory classifies fixed assets (migrated from ERP assets.AssetCategory).
type AssetCategory struct{ ent.Schema }

func (AssetCategory) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("name").NotEmpty(),
		field.Text("description").Optional(),
		field.UUID("parent_id", uuid.UUID{}).Optional().Nillable().Comment("Parent category for hierarchy"),
		field.Float("depreciation_rate").Default(0).Comment("Default annual depreciation rate %"),
		field.Int("useful_life_years").Default(5),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (AssetCategory) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
