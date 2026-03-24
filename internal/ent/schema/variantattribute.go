package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// VariantAttribute holds the schema for structured variant attribute definitions.
// Enables variant matrix generation — e.g., Size (S,M,L,XL) x Color (Red,Blue,Black)
// for fashion retailers, or Volume (250ml, 500ml, 1L) for beverage items.
type VariantAttribute struct {
	ent.Schema
}

// Fields of the VariantAttribute.
func (VariantAttribute) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("name").
			NotEmpty().
			Comment("Attribute name: Size, Color, Material, Volume"),
		field.JSON("values", []string{}).
			Comment("Allowed values: [S, M, L, XL] or [Red, Blue, Black]"),
		field.Int("sort_order").
			Default(0),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Indexes of the VariantAttribute.
func (VariantAttribute) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name").Unique(),
	}
}
