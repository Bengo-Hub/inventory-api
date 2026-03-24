package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Warranty holds the schema for warranty tracking on serialized items.
// Supports electronics shops, hardware stores, and equipment sellers.
type Warranty struct {
	ent.Schema
}

// Fields of the Warranty.
func (Warranty) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("FK to Item"),
		field.String("serial_number").
			NotEmpty().
			Comment("Item serial number"),
		field.UUID("customer_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Customer user ID from auth-service"),
		field.Time("purchase_date").
			Comment("Date of purchase"),
		field.Time("warranty_start").
			Comment("Warranty coverage start"),
		field.Time("warranty_end").
			Comment("Warranty coverage end"),
		field.Enum("status").
			Values("active", "expired", "claimed", "voided").
			Default("active"),
		field.Text("notes").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Warranty.
func (Warranty) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("warranties").
			Field("item_id").
			Unique().
			Required(),
	}
}

// Indexes of the Warranty.
func (Warranty) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "serial_number"),
		index.Fields("tenant_id", "item_id"),
		index.Fields("status"),
		index.Fields("warranty_end"),
	}
}
