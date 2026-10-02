package consumers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	posReturnDurableConsumer   = "inventory-pos-returns"
	posExchangeDurableConsumer = "inventory-pos-exchanges"
	posRestockResyncDurable    = "inventory-pos-return-restock"
	returnEventsAckWait        = 60 * time.Second
	returnEventsMaxDeliver     = 8
	returnEventsRetryDelay     = 30 * time.Second
)

// returnEventPayload is the payload of pos.return.completed and pos.exchange.completed. The
// tenant lives on the shared-events envelope; payload tenant_id is only a fallback for older
// publishers. Before 2026-10-02 pos-api never put tenant_id in the payload and this consumer
// only read it from there, so every POS return was acknowledged and dropped without a restock.
type returnEventPayload struct {
	TenantID     string `json:"tenant_id"`
	ReturnID     string `json:"return_id"`
	ReturnNumber string `json:"return_number"`
	ReturnType   string `json:"return_type"`
	// Restock=false means the goods were written off (damaged/defective/expired by default, or a
	// manager's choice): nothing goes back into stock. Absent (older events) means restock.
	Restock *bool `json:"restock"`
	OrderID      string `json:"order_id"`
	OrderNumber  string `json:"order_number"`
	CustomerName string `json:"customer_name"`
	OutletID     string `json:"outlet_id"`
	WarehouseID  string `json:"warehouse_id"`
	Lines        []struct {
		SKU        string  `json:"sku"`
		Quantity   float64 `json:"quantity"`
		OfQuantity float64 `json:"of_quantity"`
	} `json:"lines"`
}

// ReturnEventsConsumer restocks the goods of completed POS returns and exchanges, and reports
// the outcome (where the stock went) back to pos-api via inventory.return.restocked.
type ReturnEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	// hasFeature gates restocking by the same entitlement the sale consumer uses: a tenant
	// whose sales never took stock out must not get stock added back by a return.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewReturnEventsConsumer creates a new return events consumer.
func NewReturnEventsConsumer(log *zap.Logger, stockSvc *stock.Service) *ReturnEventsConsumer {
	return &ReturnEventsConsumer{
		log:      log.Named("consumers.return_events"),
		stockSvc: stockSvc,
	}
}

// SetFeatureGate wires the subscription entitlement check (fails open when unset).
func (c *ReturnEventsConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

func (c *ReturnEventsConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
}

// Start subscribes on the "pos" stream, each subject with its own durable queue group:
//   - pos.return.completed / pos.exchange.completed: the normal completion events;
//   - pos.return.restock_requested: pos-api's restock resync, same payload, consumed only here so
//     a retry never re-triggers treasury settlement or customer notifications.
//
// The idempotency key depends only on the return (type + id), so all three paths dedupe.
func (c *ReturnEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("pos"); err != nil {
		if _, err = js.AddStream(&nats.StreamConfig{
			Name:      "pos",
			Subjects:  []string{"pos.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("pos returns: ensure stream: %w", err)
		}
	}

	subs := []struct{ subject, durable string }{
		{"pos.return.completed", posReturnDurableConsumer},
		{"pos.exchange.completed", posExchangeDurableConsumer},
		{"pos.return.restock_requested", posRestockResyncDurable},
	}
	for _, sub := range subs {
		eventslib.SubscribeQueueWithRebind(
			c.log, js, "pos", sub.subject, sub.durable,
			c.handle,
			nats.Durable(sub.durable),
			nats.AckExplicit(),
			nats.AckWait(returnEventsAckWait),
			nats.MaxDeliver(returnEventsMaxDeliver),
			nats.DeliverAll(),
		)
	}
	c.log.Info("POS return/exchange restock consumers started")

	<-ctx.Done()
	return nil
}

// returnSource maps the return type to the outcome source and idempotency prefix. The prefixes
// match what this consumer always used ("pos-return-", "pos-exchange-").
func returnSource(returnType, subject string) (source, keyPrefix string) {
	if returnType == "exchange" || (returnType == "" && subject == "pos.exchange.completed") {
		return "pos_exchange", "pos-exchange-"
	}
	return "pos", "pos-return-"
}

func (c *ReturnEventsConsumer) handle(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), returnEventsAckWait-5*time.Second)
	defer cancel()

	env, p, err := eventslib.DecodeEvent[returnEventPayload](msg.Data)
	if err != nil {
		c.log.Warn("return restock: malformed event, dropping", zap.String("subject", msg.Subject), zap.Error(err))
		_ = msg.Term()
		return
	}
	source, keyPrefix := returnSource(p.ReturnType, msg.Subject)
	tenantID := env.TenantID
	if tenantID == uuid.Nil {
		tenantID, _ = uuid.Parse(p.TenantID)
	}
	returnID, rerr := uuid.Parse(p.ReturnID)
	if tenantID == uuid.Nil || rerr != nil {
		c.log.Error("return restock: event has no tenant or return id, dropping",
			zap.String("source", source), zap.String("return_id", p.ReturnID))
		_ = msg.Term()
		return
	}
	log := c.log.With(zap.String("tenant_id", tenantID.String()), zap.String("return_id", p.ReturnID),
		zap.String("return_number", p.ReturnNumber), zap.String("source", source))

	if p.Restock != nil && !*p.Restock {
		log.Info("return restock: goods written off, stock left unchanged")
		_ = msg.Ack()
		return
	}

	if !c.entitled(ctx, tenantID) {
		log.Info("return restock: tenant not entitled to inventory sync, skipping")
		c.reportOutcome(ctx, log, tenantID, returnID, source, nil, "skipped_not_entitled", "")
		_ = msg.Ack()
		return
	}

	req := stock.ReturnRestockRequest{
		ReturnID:           returnID,
		ReturnNumber:       p.ReturnNumber,
		OrderID:            parseUUIDOrNil(p.OrderID),
		OrderNumber:        p.OrderNumber,
		CustomerName:       p.CustomerName,
		OutletID:           parseUUIDOrNil(p.OutletID),
		WarehouseID:        parseUUIDOrNil(p.WarehouseID),
		IdempotencyKey:     keyPrefix + returnID.String(),
		AllowDirectRestock: true,
	}
	for _, l := range p.Lines {
		req.Lines = append(req.Lines, stock.ReturnRestockLine{SKU: l.SKU, Quantity: l.Quantity, OfQuantity: l.OfQuantity})
	}

	result, err := c.stockSvc.RestockReturn(ctx, tenantID, req)
	if err != nil {
		lastTry := false
		if md, merr := msg.Metadata(); merr == nil && md.NumDelivered >= returnEventsMaxDeliver {
			lastTry = true
		}
		log.Error("return restock failed", zap.Bool("last_attempt", lastTry), zap.Error(err))
		if lastTry {
			// Tell pos-api so the return shows "restock failed" with a retry action instead of
			// silently looking done. A retry re-publishes the event; restock is idempotent.
			c.reportOutcome(ctx, log, tenantID, returnID, source, nil, "failed", err.Error())
			_ = msg.Term()
			return
		}
		_ = msg.NakWithDelay(returnEventsRetryDelay)
		return
	}

	log.Info("return restock processed", zap.String("status", result.Status),
		zap.Int("lines", len(result.Lines)), zap.Strings("skipped", result.Skipped))
	c.reportOutcome(ctx, log, tenantID, returnID, source, result, result.Status, "")
	_ = msg.Ack()
}

// reportOutcome is best-effort: the restock itself already committed, and a replay of the
// source event re-reports the same outcome idempotently.
func (c *ReturnEventsConsumer) reportOutcome(ctx context.Context, log *zap.Logger, tenantID, returnID uuid.UUID, source string, result *stock.ReturnRestockResult, status, errMsg string) {
	if err := c.stockSvc.PublishReturnRestockOutcome(ctx, tenantID, returnID, source, result, status, errMsg); err != nil {
		log.Warn("return restock: outcome event not written", zap.Error(err))
	}
}

func parseUUIDOrNil(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}
