package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Supplier holds the schema for vendor/supplier management.
// Enables purchase order workflows for all industry types.
type Supplier struct {
	ent.Schema
}

// Fields of the Supplier.
func (Supplier) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("name").
			NotEmpty(),
		field.String("code").
			NotEmpty().
			Comment("Short supplier code for reference"),
		field.String("contact_name").
			Optional(),
		field.String("contact_email").
			Optional(),
		field.String("contact_phone").
			Optional(),
		field.Text("address").
			Optional(),
		field.String("payment_terms").
			Optional().
			Comment("Net30, Net60, COD, etc."),
		field.Bool("is_active").
			Default(true),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Supplier.
func (Supplier) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("purchase_orders", PurchaseOrder.Type),
	}
}

// Indexes of the Supplier.
func (Supplier) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
