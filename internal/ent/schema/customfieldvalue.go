package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// CustomFieldValue holds the schema for actual values of custom fields on items.
type CustomFieldValue struct {
	ent.Schema
}

// Fields of the CustomFieldValue.
func (CustomFieldValue) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item"),
		field.UUID("field_definition_id", uuid.UUID{}).
			Comment("FK to CustomFieldDefinition"),
		field.String("value").
			Comment("Stored as string, validated against field_type of definition"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the CustomFieldValue.
func (CustomFieldValue) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("custom_field_values").
			Field("item_id").
			Unique().
			Required(),
		edge.From("definition", CustomFieldDefinition.Type).
			Ref("values").
			Field("field_definition_id").
			Unique().
			Required(),
	}
}

// Indexes of the CustomFieldValue.
func (CustomFieldValue) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("item_id", "field_definition_id").Unique(),
		index.Fields("item_id"),
	}
}
