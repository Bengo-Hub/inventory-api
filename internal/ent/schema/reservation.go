package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ReservedItemJSON is the JSON structure for items within a reservation.
// Quantities are float64 to support fractional stock (e.g. 0.5 L).
type ReservedItemJSON struct {
	SKU             string  `json:"sku"`
	RequestedQty    float64 `json:"requested_qty"`
	ReservedQty     float64 `json:"reserved_qty"`
	AvailableQty    float64 `json:"available_qty"`
	IsFullyReserved bool    `json:"is_fully_reserved"`
}

// Reservation holds the schema definition for stock reservations.
type Reservation struct {
	ent.Schema
}

// Fields of the Reservation.
func (Reservation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("order_id", uuid.UUID{}).
			Comment("The order this reservation belongs to"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to warehouses table"),
		field.String("status").
			Default("pending").
			Comment("pending, confirmed, released, consumed"),
		field.JSON("items", []ReservedItemJSON{}).
			Default([]ReservedItemJSON{}).
			Comment("Reserved items with quantities"),
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("When this reservation expires if not confirmed"),
		field.Time("confirmed_at").
			Optional().
			Nillable(),
		field.String("idempotency_key").
			Optional().
			Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Reservation.
func (Reservation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("warehouse", Warehouse.Type).
			Ref("reservations").
			Field("warehouse_id").
			Unique(),
	}
}

// Indexes of the Reservation.
func (Reservation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "order_id"),
		index.Fields("tenant_id", "status"),
		index.Fields("idempotency_key").Unique(),
	}
}
