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
	// ShortfallQty is how much of the deduction could NOT be satisfied because on-hand
	// stock was lower than the theoretical need (balances floor at zero). Feeds the
	// actual-vs-theoretical variance reports; 0/omitted when fully satisfied.
	ShortfallQty float64 `json:"shortfall_qty,omitempty"`
	// UnitMismatch marks a recipe line whose unit could not be converted to the
	// ingredient's stock unit (cross-dimension, e.g. ml of an item stocked in pieces
	// with no unit_content declared). The line is recorded for visibility but NO
	// stock was deducted — deducting a raw number would corrupt the balance.
	UnitMismatch bool `json:"unit_mismatch,omitempty"`
	// Theoretical marks a line for a non-depleting item: usage is recorded for
	// AvT/food-cost reporting but no balance was decremented.
	Theoretical bool `json:"theoretical,omitempty"`
	// RequestedUOM is the recipe-line unit the quantity was originally expressed in,
	// kept when it differs from the item's stock unit (post-conversion audit trail).
	RequestedUOM string `json:"requested_uom,omitempty"`
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
