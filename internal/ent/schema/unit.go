package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Unit holds the schema definition for units of measure.
type Unit struct {
	ent.Schema
}

// Fields of the Unit.
func (Unit) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("name").
			NotEmpty().
			Unique().
			Comment("Unit name, e.g., KG, Gram, Piece"),
		field.String("abbreviation").
			Optional().
			Comment("Short name, e.g., kg, g, pc"),
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

// Edges of the Unit.
func (Unit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("items", Item.Type).
			Ref("units"),
	}
}

// Indexes of the Unit.
func (Unit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}
