package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// CustomFieldDefinition holds the schema for tenant-defined custom fields.
// Enables structured metadata per item category or tenant — e.g., serial_number for electronics,
// expiry_date for pharmacy, color for fashion.
type CustomFieldDefinition struct {
	ent.Schema
}

// Fields of the CustomFieldDefinition.
func (CustomFieldDefinition) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("field_key").
			NotEmpty().
			Comment("Machine-readable key: serial_number, expiry_date, color"),
		field.String("label").
			NotEmpty().
			Comment("Display label: Serial Number, Expiry Date, Color"),
		field.Enum("field_type").
			Values("text", "number", "date", "boolean", "enum", "url").
			Default("text").
			Comment("Data type for validation"),
		field.JSON("enum_values", []string{}).
			Optional().
			Comment("Allowed values when field_type=enum"),
		field.Bool("is_required").
			Default(false),
		field.UUID("category_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("If set, only applies to items in this category"),
		field.Int("sort_order").
			Default(0),
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

// Edges of the CustomFieldDefinition.
func (CustomFieldDefinition) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("values", CustomFieldValue.Type),
		edge.From("category", ItemCategory.Type).
			Ref("custom_field_definitions").
			Field("category_id").
			Unique(),
	}
}

// Indexes of the CustomFieldDefinition.
func (CustomFieldDefinition) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "field_key").Unique(),
		index.Fields("tenant_id", "category_id"),
		index.Fields("tenant_id", "is_active"),
	}
}
