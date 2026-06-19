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
	// hasFeature gates cross-service stock sync by subscription entitlement. When set,
	// reservations are only consumed/released for tenants entitled to basic_inventory_access
	// (the cross-service inventory-access feature auto-injected into POS/ordering plans — the
	// same code the POS→inventory direction gates on). Nil → no gating (fail open).
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewOrderEventsConsumer creates a new consumer.
func NewOrderEventsConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *OrderEventsConsumer {
	return &OrderEventsConsumer{
		log:      log.Named("consumers.order_events"),
		stockSvc: stockSvc,
		orm:      orm,
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate stock sync.
func (c *OrderEventsConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may have ordering events drive its inventory stock.
// Fails open when no gate is wired.
func (c *OrderEventsConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
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

	eventslib.SubscribeWithRebind(
		c.log,
		js,
		"ordering.order.*",
		c.handleMessage,
		nats.Durable(orderEventsDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(orderEventsAckWait),
		nats.MaxDeliver(orderEventsMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("order events consumer started", zap.String("durable", orderEventsDurableConsumer))

	<-ctx.Done()
	return nil
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

	// Gate cross-service stock sync by subscription entitlement (fails open if no gate
	// wired / subscriptions-api down). Ack + skip when the tenant lacks basic_inventory_access.
	if !c.entitled(ctx, tenantID) {
		c.log.Debug("order events: tenant lacks basic_inventory_access — skipping reservation sync",
			zap.String("tenant_id", tenantID.String()), zap.String("order_id", orderID.String()))
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
