package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemBrand holds the schema definition for item brands.
// Tenant-scoped, mirrors ItemCategory: items optionally reference a brand
// (e.g. Coca-Cola, Samsung, Nestlé) so the POS/ordering UIs can offer a Brands tab.
type ItemBrand struct {
	ent.Schema
}

// Fields of the ItemBrand.
func (ItemBrand) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("name").
			NotEmpty(),
		field.String("code").
			Comment("URL-safe slug, unique per tenant (e.g. coca-cola, samsung)"),
		field.Text("description").
			Optional(),
		field.String("logo_url").
			Optional().
			Comment("Brand logo image URL for display in the Brands tab"),
		field.Int("sort_order").
			Default(0).
			Comment("Display ordering"),
		field.Bool("is_active").
			Default(true),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ItemBrand.
func (ItemBrand) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("items", Item.Type),
	}
}

// Indexes of the ItemBrand.
func (ItemBrand) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name"),
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "sort_order"),
	}
}
