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
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	posReturnDurableConsumer      = "inventory-pos-returns"
	orderingReturnDurableConsumer = "inventory-ordering-returns"
	returnEventsAckWait           = 30 * time.Second
	returnEventsMaxDeliver        = 5
)

// returnLineItem is a line from the return event payload.
type returnLineItem struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// ReturnEventsConsumer handles pos.return.completed and ordering.return.approved events
// to restock inventory for returned items.
type ReturnEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
}

// NewReturnEventsConsumer creates a new return events consumer.
func NewReturnEventsConsumer(log *zap.Logger, stockSvc *stock.Service) *ReturnEventsConsumer {
	return &ReturnEventsConsumer{
		log:      log.Named("consumers.return_events"),
		stockSvc: stockSvc,
	}
}

// StartPOSReturns subscribes to pos.return.completed events.
func (c *ReturnEventsConsumer) StartPOSReturns(ctx context.Context, js nats.JetStreamContext) error {
	_, err := js.StreamInfo("pos")
	if err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "pos",
			Subjects:  []string{"pos.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("pos returns: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"pos",
		"pos.return.completed",
		posReturnDurableConsumer,
		c.handlePOSReturn,
		nats.Durable(posReturnDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(returnEventsAckWait),
		nats.MaxDeliver(returnEventsMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("POS return events consumer started", zap.String("durable", posReturnDurableConsumer))

	<-ctx.Done()
	return nil
}

// StartOrderingReturns subscribes to ordering.return.approved events.
func (c *ReturnEventsConsumer) StartOrderingReturns(ctx context.Context, js nats.JetStreamContext) error {
	_, err := js.StreamInfo("ordering")
	if err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "ordering",
			Subjects:  []string{"ordering.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("ordering returns: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"ordering",
		"ordering.return.approved",
		orderingReturnDurableConsumer,
		c.handleOrderingReturn,
		nats.Durable(orderingReturnDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(returnEventsAckWait),
		nats.MaxDeliver(returnEventsMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("ordering return events consumer started", zap.String("durable", orderingReturnDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *ReturnEventsConsumer) handlePOSReturn(msg *nats.Msg) {
	ctx := context.Background()
	var envelope struct {
		Payload struct {
			TenantID    string           `json:"tenant_id"`
			ReturnID    string           `json:"return_id"`
			WarehouseID string           `json:"warehouse_id"`
			Lines       []returnLineItem `json:"lines"`
		} `json:"payload"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("pos return: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("pos return: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack()
		return
	}

	var warehouseID uuid.UUID
	if envelope.Payload.WarehouseID != "" {
		warehouseID, _ = uuid.Parse(envelope.Payload.WarehouseID)
	}

	items := make([]stock.RestockItem, 0, len(envelope.Payload.Lines))
	for _, l := range envelope.Payload.Lines {
		items = append(items, stock.RestockItem{SKU: l.SKU, Quantity: l.Quantity})
	}

	if len(items) == 0 {
		_ = msg.Ack()
		return
	}

	idempKey := fmt.Sprintf("pos-return-%s", envelope.Payload.ReturnID)
	if err := c.stockSvc.RestockItems(ctx, tenantID, warehouseID, items, idempKey); err != nil {
		c.log.Error("pos return: restock failed",
			zap.Error(err),
			zap.String("return_id", envelope.Payload.ReturnID),
		)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (c *ReturnEventsConsumer) handleOrderingReturn(msg *nats.Msg) {
	ctx := context.Background()
	var envelope struct {
		Payload struct {
			TenantID    string           `json:"tenant_id"`
			ReturnID    string           `json:"return_id"`
			WarehouseID string           `json:"warehouse_id"`
			Lines       []returnLineItem `json:"lines"`
		} `json:"payload"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("ordering return: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("ordering return: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack()
		return
	}

	var warehouseID uuid.UUID
	if envelope.Payload.WarehouseID != "" {
		warehouseID, _ = uuid.Parse(envelope.Payload.WarehouseID)
	}

	items := make([]stock.RestockItem, 0, len(envelope.Payload.Lines))
	for _, l := range envelope.Payload.Lines {
		items = append(items, stock.RestockItem{SKU: l.SKU, Quantity: l.Quantity})
	}

	if len(items) == 0 {
		_ = msg.Ack()
		return
	}

	idempKey := fmt.Sprintf("ordering-return-%s", envelope.Payload.ReturnID)
	if err := c.stockSvc.RestockItems(ctx, tenantID, warehouseID, items, idempKey); err != nil {
		c.log.Error("ordering return: restock failed",
			zap.Error(err),
			zap.String("return_id", envelope.Payload.ReturnID),
		)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}
