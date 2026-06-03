package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Ticket holds the schema definition for an event ticket sold against an event Item
// (type=SERVICE with total_capacity/booked_capacity). Tickets are tenant business data.
type Ticket struct {
	ent.Schema
}

// Fields of the Ticket.
func (Ticket) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("event_item_id", uuid.UUID{}).
			Comment("FK to the event Item (type=SERVICE)"),
		field.UUID("order_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Originating order from ordering-service, when purchased"),
		field.String("tier_id").
			Optional().
			Comment("Tier id from Item.metadata.ticket_tiers"),
		field.String("tier_name").
			Optional().
			Comment("Tier display name snapshot"),
		field.UUID("buyer_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Buyer user id, when known"),
		field.String("buyer_name").
			Optional(),
		field.String("buyer_email").
			Optional(),
		field.Int("quantity").
			Default(1).
			Comment("Seats covered by this ticket (each seat decrements capacity)"),
		field.Float("unit_price").
			Default(0),
		field.Float("total_price").
			Default(0),
		field.String("currency").
			Default("KES"),
		field.String("code").
			NotEmpty().
			Comment("Unique redemption code (rendered as QR), unique per tenant"),
		field.Enum("status").
			Values("issued", "redeemed", "cancelled", "refunded", "void").
			Default("issued"),
		field.Time("valid_from").
			Optional().
			Nillable(),
		field.Time("valid_until").
			Optional().
			Nillable(),
		field.Time("redeemed_at").
			Optional().
			Nillable(),
		field.UUID("redeemed_by", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("User who scanned/redeemed the ticket"),
		field.String("check_in_location").
			Optional(),
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

// Indexes of the Ticket.
func (Ticket) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "event_item_id", "status"),
		index.Fields("tenant_id", "buyer_id", "status"),
		index.Fields("tenant_id", "order_id"),
	}
}
