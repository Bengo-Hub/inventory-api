package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entticket "github.com/bengobox/inventory-service/internal/ent/ticket"
	"github.com/bengobox/inventory-service/internal/modules/tickets"
)

const (
	ticketIssuanceDurable    = "inventory-ticket-issuance"
	ticketIssuanceAckWait    = 30 * time.Second
	ticketIssuanceMaxDeliver = 5
)

// TicketIssuanceConsumer listens for ordering.order.payment_confirmed and issues event tickets for
// any paid line whose SKU resolves to a SERVICE-type event Item (with capacity). Idempotent per order.
type TicketIssuanceConsumer struct {
	log        *zap.Logger
	ticketsSvc *tickets.Service
	orm        *ent.Client
}

// NewTicketIssuanceConsumer constructs the consumer.
func NewTicketIssuanceConsumer(log *zap.Logger, ticketsSvc *tickets.Service, orm *ent.Client) *TicketIssuanceConsumer {
	return &TicketIssuanceConsumer{
		log:        log.Named("consumers.ticket_issuance"),
		ticketsSvc: ticketsSvc,
		orm:        orm,
	}
}

// Start subscribes to ordering.order.payment_confirmed via a durable JetStream consumer.
func (c *TicketIssuanceConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("ordering"); err != nil {
		if _, aerr := js.AddStream(&nats.StreamConfig{
			Name:      "ordering",
			Subjects:  []string{"ordering.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); aerr != nil && aerr != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("ticket issuance: ensure stream: %w", aerr)
		}
	}
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"ordering",
		"ordering.order.payment_confirmed",
		ticketIssuanceDurable,
		c.handleMessage,
		nats.Durable(ticketIssuanceDurable),
		nats.AckExplicit(),
		nats.AckWait(ticketIssuanceAckWait),
		nats.MaxDeliver(ticketIssuanceMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("ticket issuance consumer started", zap.String("durable", ticketIssuanceDurable))
	<-ctx.Done()
	return nil
}

// paymentConfirmedEnvelope decodes the ordering.order.payment_confirmed event.
// ordering-backend now publishes the fleet-uniform shared-events envelope
// (tenant_id/payload).
type paymentConfirmedEnvelope struct {
	TenantID string `json:"tenant_id"`
	Data     struct {
		OrderID       string `json:"order_id"`
		CustomerEmail string `json:"customer_email"`
		CustomerName  string `json:"customer_name"`
		Lines         []struct {
			SKU       string         `json:"sku"`
			Name      string         `json:"name"`
			Quantity  int            `json:"quantity"`
			UnitPrice float64        `json:"unit_price"`
			Metadata  map[string]any `json:"metadata"`
		} `json:"lines"`
	} `json:"payload"` // shared-events nests business fields under `payload`, not `data`
}

// parseAttendees decodes the per-line `attendees` metadata (a JSON array of {name,email})
// set by the storefront checkout into typed Attendee values. Tolerant of the map[string]any
// shape JSON unmarshalling produces; returns nil when absent/malformed so issuance falls back
// to a single buyer-named ticket for the line quantity.
func parseAttendees(v any) []tickets.Attendee {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	out := make([]tickets.Attendee, 0, len(arr))
	for _, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		email, _ := m["email"].(string)
		if name == "" && email == "" {
			continue
		}
		out = append(out, tickets.Attendee{Name: name, Email: email})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *TicketIssuanceConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var env paymentConfirmedEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn("ticket issuance: unmarshal failed", zap.Error(err))
		_ = msg.Ack() // malformed — don't retry
		return
	}
	tenantID, err := uuid.Parse(env.TenantID)
	if err != nil {
		_ = msg.Ack()
		return
	}
	orderID, err := uuid.Parse(env.Data.OrderID)
	if err != nil {
		_ = msg.Ack()
		return
	}

	for _, line := range env.Data.Lines {
		if line.SKU == "" || line.Quantity <= 0 {
			continue
		}
		// Resolve the SKU to an Item; only SERVICE-type events with capacity are ticketable.
		evItem, err := c.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(line.SKU)).
			Only(ctx)
		if err != nil {
			continue // not an inventory item we own; skip
		}
		if evItem.Type != entitem.TypeSERVICE || evItem.TotalCapacity == nil || *evItem.TotalCapacity <= 0 {
			continue // not an event
		}

		// Idempotency: skip if tickets already issued for this order + event (handles redelivery).
		exists, err := c.orm.Ticket.Query().
			Where(
				entticket.TenantID(tenantID),
				entticket.OrderID(orderID),
				entticket.EventItemID(evItem.ID),
			).
			Exist(ctx)
		if err != nil {
			c.log.Error("ticket issuance: idempotency check failed", zap.Error(err))
			_ = msg.Nak()
			return
		}
		if exists {
			continue
		}

		tierID, _ := line.Metadata["tier_id"].(string)
		tierName, _ := line.Metadata["tier_name"].(string)
		attendees := parseAttendees(line.Metadata["attendees"])
		oid := orderID
		if _, err := c.ticketsSvc.IssueTicket(ctx, tenantID, tickets.IssueInput{
			EventItemID: evItem.ID,
			OrderID:     &oid,
			TierID:      tierID,
			TierName:    tierName,
			Quantity:    line.Quantity,
			Attendees:   attendees,
			UnitPrice:   line.UnitPrice,
			BuyerName:   env.Data.CustomerName,
			BuyerEmail:  env.Data.CustomerEmail,
		}); err != nil {
			c.log.Error("ticket issuance: issue failed",
				zap.String("order_id", orderID.String()), zap.String("sku", line.SKU), zap.Error(err))
			_ = msg.Nak()
			return
		}
		c.log.Info("ticket issued from paid order",
			zap.String("order_id", orderID.String()), zap.String("sku", line.SKU), zap.Int("qty", line.Quantity))
	}
	_ = msg.Ack()
}
