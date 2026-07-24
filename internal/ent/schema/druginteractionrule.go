package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DrugInteractionRule holds a curated drug-drug-interaction pair used by the pharmacy
// (DAWA use-case) clinical safety check. Platform-default rows use tenant_id=uuid.Nil +
// is_global=true, exactly mirroring ItemCategory's existing global/tenant-override pattern
// (see items.Service.ListCategories) — a tenant sees its own rows plus the global set.
type DrugInteractionRule struct{ ent.Schema }

func (DrugInteractionRule) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("uuid.Nil for platform-default (is_global) rows"),
		field.Bool("is_global").
			Default(false).
			Comment("Platform-default rule visible to all tenants (tenant_id=uuid.Nil)"),
		field.String("class_a").
			NotEmpty().
			Comment("Item.drug_class key — stored alphabetically before class_b to dedupe A/B vs B/A"),
		field.String("class_b").
			NotEmpty(),
		field.Enum("severity").
			Values("minor", "moderate", "major", "contraindicated").
			Default("moderate"),
		field.Text("description").
			Optional(),
		field.Text("clinical_recommendation").
			Optional(),
		field.String("source").
			Optional().
			Comment("Citation/reference for this pair, e.g. 'in-house-v1'"),
		field.Bool("is_active").
			Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DrugInteractionRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "class_a", "class_b").Unique(),
		index.Fields("tenant_id", "is_active"),
		index.Fields("is_global"),
	}
}
