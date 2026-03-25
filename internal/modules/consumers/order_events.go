package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/reservation"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	orderEventsDurableConsumer = "inventory-service-order-events"
	orderEventsAckWait         = 30 * time.Second
	orderEventsMaxDeliver      = 5
)

// orderEventPayload is the minimal fields we need from ordering events.
type orderEventPayload struct {
	TenantID string `json:"tenant_id"`
	OrderID  string `json:"order_id"`
	Status   string `json:"status"`
}

// OrderEventsConsumer consumes ordering lifecycle events to auto-manage stock reservations.
type OrderEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	orm      *ent.Client
}

// NewOrderEventsConsumer creates a new consumer.
func NewOrderEventsConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *OrderEventsConsumer {
	return &OrderEventsConsumer{
		log:      log.Named("consumers.order_events"),
		stockSvc: stockSvc,
		orm:      orm,
	}
}

// Start begins listening for ordering events via JetStream durable consumer.
func (c *OrderEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "ordering" stream exists (it's created by ordering-backend, but may not exist yet)
	_, err := js.StreamInfo("ordering")
	if err != nil {
		c.log.Info("ordering stream not found, creating it for consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "ordering",
			Subjects:  []string{"ordering.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("order events: ensure stream: %w", err)
		}
	}

	sub, err := js.Subscribe(
		"ordering.order.*",
		c.handleMessage,
		nats.Durable(orderEventsDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(orderEventsAckWait),
		nats.MaxDeliver(orderEventsMaxDeliver),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("order events: subscribe: %w", err)
	}
	c.log.Info("order events consumer started", zap.String("durable", orderEventsDurableConsumer))

	<-ctx.Done()
	return sub.Unsubscribe()
}

func (c *OrderEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	// The payload is a shared-events Event JSON; extract inner payload fields
	var envelope struct {
		Payload struct {
			TenantID string `json:"tenant_id"`
			OrderID  string `json:"order_id"`
			Status   string `json:"status"`
		} `json:"payload"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("order events: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("order events: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack() // don't retry malformed messages
		return
	}
	orderID, err := uuid.Parse(envelope.Payload.OrderID)
	if err != nil {
		c.log.Warn("order events: invalid order_id", zap.String("raw", envelope.Payload.OrderID))
		_ = msg.Ack()
		return
	}

	switch envelope.EventType {
	case "order.completed":
		if err := c.autoConsumeReservation(ctx, tenantID, orderID); err != nil {
			c.log.Error("order events: auto-consume failed", zap.Error(err), zap.String("order_id", orderID.String()))
			_ = msg.Nak()
			return
		}
	case "order.cancelled":
		if err := c.autoReleaseReservation(ctx, tenantID, orderID); err != nil {
			c.log.Error("order events: auto-release failed", zap.Error(err), zap.String("order_id", orderID.String()))
			_ = msg.Nak()
			return
		}
	default:
		// Ignore other order events
	}

	_ = msg.Ack()
}

// autoConsumeReservation finds the active reservation for the order and consumes it.
func (c *OrderEventsConsumer) autoConsumeReservation(ctx context.Context, tenantID, orderID uuid.UUID) error {
	resv, err := c.orm.Reservation.Query().
		Where(
			reservation.TenantID(tenantID),
			reservation.OrderID(orderID),
			reservation.StatusIn("pending", "confirmed"),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			c.log.Info("order events: no active reservation for completed order (already consumed or never reserved)",
				zap.String("order_id", orderID.String()))
			return nil
		}
		return fmt.Errorf("query reservation: %w", err)
	}

	if err := c.stockSvc.ConsumeReservation(ctx, tenantID, resv.ID); err != nil {
		return fmt.Errorf("consume reservation %s: %w", resv.ID, err)
	}

	c.log.Info("auto-consumed reservation on order completion",
		zap.String("reservation_id", resv.ID.String()),
		zap.String("order_id", orderID.String()),
	)
	return nil
}

// autoReleaseReservation finds the active reservation for the order and releases it.
func (c *OrderEventsConsumer) autoReleaseReservation(ctx context.Context, tenantID, orderID uuid.UUID) error {
	resv, err := c.orm.Reservation.Query().
		Where(
			reservation.TenantID(tenantID),
			reservation.OrderID(orderID),
			reservation.StatusIn("pending", "confirmed"),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			c.log.Info("order events: no active reservation for cancelled order",
				zap.String("order_id", orderID.String()))
			return nil
		}
		return fmt.Errorf("query reservation: %w", err)
	}

	if err := c.stockSvc.ReleaseReservation(ctx, tenantID, resv.ID, "order_cancelled"); err != nil {
		return fmt.Errorf("release reservation %s: %w", resv.ID, err)
	}

	c.log.Info("auto-released reservation on order cancellation",
		zap.String("reservation_id", resv.ID.String()),
		zap.String("order_id", orderID.String()),
	)
	return nil
}
