package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ConsumptionItemJSON is the JSON structure for consumed items.
type ConsumptionItemJSON struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// Consumption holds the schema definition for stock consumption records.
type Consumption struct {
	ent.Schema
}

// Fields of the Consumption.
func (Consumption) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("order_id", uuid.UUID{}).
			Comment("The order that triggered consumption"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.JSON("items", []ConsumptionItemJSON{}).
			Default([]ConsumptionItemJSON{}),
		field.String("reason").
			Default("sale").
			Comment("sale, waste, adjustment, transfer"),
		field.String("status").
			Default("processed"),
		field.String("idempotency_key").
			Optional().
			Nillable(),
		field.Time("processed_at").
			Default(time.Now),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the Consumption.
func (Consumption) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "order_id"),
		index.Fields("idempotency_key").Unique(),
	}
}
