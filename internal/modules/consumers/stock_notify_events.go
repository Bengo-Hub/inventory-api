package consumers

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	notifmod "github.com/bengobox/inventory-service/internal/modules/notifications"
)

const (
	stockNotifyDurableConsumer = "inventory-stock-notify-push"
	stockNotifyAckWait         = 30 * time.Second
	stockNotifyMaxDeliver      = 5
)

// StockNotifyEventsConsumer bridges inventory-api's own already-published stock-change events
// (inventory.stock.updated/low/out) to a live WebSocket push for inventory-ui — a separate,
// distinctly-durable consumer from StockEventsConsumer's inventory.stock.low → auto-reorder
// business logic (a different concern; the fleet's uniform-consumer rule requires each logical
// consumer to own its own durable/queue name rather than share one).
type StockNotifyEventsConsumer struct {
	log *zap.Logger
	hub *notifmod.Hub
}

// NewStockNotifyEventsConsumer creates a new StockNotifyEventsConsumer.
func NewStockNotifyEventsConsumer(log *zap.Logger, hub *notifmod.Hub) *StockNotifyEventsConsumer {
	return &StockNotifyEventsConsumer{log: log.Named("consumers.stock_notify_events"), hub: hub}
}

// Start subscribes to inventory.stock.{updated,low,out} via JetStream durable consumers.
func (c *StockNotifyEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("inventory"); err != nil {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:      "inventory",
			Subjects:  []string{"inventory.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("stock notify events: ensure stream: %w", err)
		}
	}

	subjects := []string{"inventory.stock.updated", "inventory.stock.low", "inventory.stock.out"}
	for _, subject := range subjects {
		// One durable per subject (subject itself is part of the durable name) — a JetStream
		// durable's filter subject is immutable, and this mirrors the existing per-subject
		// durable-naming convention already used fleet-wide (see reference_shared_events_subject
		// _convention.md's "new durable name per subject" rule).
		durable := stockNotifyDurableConsumer + "-" + subjectSuffix(subject)
		eventslib.SubscribeQueueWithRebind(
			c.log, js, "inventory", subject, durable, c.handleMessage,
			nats.Durable(durable),
			nats.AckExplicit(),
			nats.AckWait(stockNotifyAckWait),
			nats.MaxDeliver(stockNotifyMaxDeliver),
			nats.DeliverAll(),
		)
	}
	c.log.Info("stock notify events consumer started", zap.Strings("subjects", subjects))

	<-ctx.Done()
	return nil
}

func subjectSuffix(subject string) string {
	switch subject {
	case "inventory.stock.updated":
		return "updated"
	case "inventory.stock.low":
		return "low"
	case "inventory.stock.out":
		return "out"
	default:
		return "other"
	}
}

func (c *StockNotifyEventsConsumer) handleMessage(msg *nats.Msg) {
	defer func() {
		if rec := recover(); rec != nil {
			c.log.Error("stock notify events: handler panicked — dropping event", zap.Any("panic", rec))
			_ = msg.Term()
		}
	}()

	evt, err := eventslib.FromJSON(msg.Data)
	if err != nil {
		c.log.Warn("stock notify events: unmarshal envelope failed", zap.Error(err))
		_ = msg.Nak()
		return
	}
	if evt.TenantID.String() == "00000000-0000-0000-0000-000000000000" {
		_ = msg.Ack()
		return
	}
	itemID, _ := evt.Payload["item_id"].(string)
	sku, _ := evt.Payload["sku"].(string)

	// Thin invalidation nudge only (type + ids) — inventory-ui refetches the real stock figures
	// from its own REST endpoint; this push never carries authoritative data itself.
	c.hub.BroadcastToTenant(evt.TenantID, notifmod.Message{
		Type:    "stock_changed",
		Payload: map[string]any{"tenant_id": evt.TenantID.String(), "item_id": itemID, "sku": sku},
	})

	_ = msg.Ack()
}
