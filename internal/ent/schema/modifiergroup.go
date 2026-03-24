package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ModifierGroup holds the schema definition for modifier groups (e.g. Size, Extras, Toppings).
type ModifierGroup struct {
	ent.Schema
}

// Fields of the ModifierGroup.
func (ModifierGroup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item this modifier group belongs to"),
		field.String("name").
			NotEmpty().
			Comment("e.g. Size, Extras, Toppings"),
		field.Bool("is_required").
			Default(false).
			Comment("Must select at least min_selections"),
		field.Int("min_selections").
			Default(0),
		field.Int("max_selections").
			Default(1),
		field.Int("display_order").
			Default(0),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ModifierGroup.
func (ModifierGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("options", ModifierOption.Type),
		edge.From("item", Item.Type).
			Ref("modifier_groups").
			Unique().
			Required().
			Field("item_id"),
	}
}

// Indexes of the ModifierGroup.
func (ModifierGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id"),
		index.Fields("item_id", "display_order"),
	}
}
