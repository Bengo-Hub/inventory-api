package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AssetInsurance tracks insurance policies for assets (ERP assets.AssetInsurance).
type AssetInsurance struct{ ent.Schema }

func (AssetInsurance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("asset_id", uuid.UUID{}),
		field.String("policy_number").NotEmpty(),
		field.String("provider").Optional(),
		field.String("policy_type").Optional(),
		field.Float("coverage_amount").Default(0),
		field.Float("premium_amount").Default(0),
		field.Time("start_date"),
		field.Time("end_date"),
		field.Float("deductible").Default(0),
		field.Bool("is_active").Default(true),
		field.Text("notes").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AssetInsurance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_id"),
		index.Fields("tenant_id", "policy_number").Unique(),
	}
}
