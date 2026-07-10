package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ModifierOption holds the schema definition for individual options within a modifier group.
type ModifierOption struct {
	ent.Schema
}

// Fields of the ModifierOption.
func (ModifierOption) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("group_id", uuid.UUID{}).
			Comment("FK to ModifierGroup"),
		field.String("name").
			NotEmpty().
			Comment("e.g. Large, Extra Cheese"),
		field.String("sku").
			Optional().
			Comment("Inventory SKU if this modifier consumes stock"),
		field.Float("price_adjustment").
			Default(0).
			Comment("Price delta: +50 for Large, -10 for No Sauce"),
		field.Float("deduction_qty").
			Default(1).
			Comment("How much of the linked SKU one selection of this option consumes per sold unit of the parent line (e.g. 20 for 20g of honey on 'Extra Honey'). Defaults to 1 (a single natural unit of the SKU) when the option was never given a specific amount."),
		field.String("deduction_unit").
			Optional().
			Comment("Unit deduction_qty is expressed in (e.g. 'g', 'ml'). Empty means the linked item's own natural stock unit."),
		field.Bool("is_default").
			Default(false),
		field.Bool("is_active").
			Default(true),
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

// Edges of the ModifierOption.
func (ModifierOption) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("group", ModifierGroup.Type).
			Ref("options").
			Field("group_id").
			Unique().
			Required(),
	}
}

// Indexes of the ModifierOption.
func (ModifierOption) Indexes() []ent.Index {
	return []ent.Index{
		// Unique per group — prevents duplicate option names on re-upload (idempotency).
		index.Fields("group_id", "name").Unique(),
		index.Fields("group_id", "display_order"),
	}
}
