package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemTranslation holds localized content for items.
type ItemTranslation struct {
	ent.Schema
}

// Fields of the ItemTranslation.
func (ItemTranslation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("Referenced item"),
		field.String("locale").
			NotEmpty().
			Comment("Locale code, e.g., en, sw"),
		field.String("name").
			NotEmpty(),
		field.Text("description").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ItemTranslation.
func (ItemTranslation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("translations").
			Unique().
			Required().
			Field("item_id"),
	}
}

// Indexes of the ItemTranslation.
func (ItemTranslation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("item_id", "locale").Unique(),
	}
}
