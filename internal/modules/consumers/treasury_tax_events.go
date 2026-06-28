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
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

const (
	treasuryTaxDurableConsumer = "inventory-treasury-tax-cache-invalidate"
	treasuryTaxAckWait         = 30 * time.Second
	treasuryTaxMaxDeliver      = 3
)

// treasuryTaxPayload is the inner payload of a treasury.tax.code_updated event.
type treasuryTaxPayload struct {
	TenantID string `json:"tenant_id"`
	Code     string `json:"code"`
	Action   string `json:"action"`
}

// TreasuryTaxEventsConsumer listens for treasury tax-code changes and invalidates the
// locally cached treasury tax data so rate changes propagate immediately instead of
// waiting for the ~10-minute cache TTL.
type TreasuryTaxEventsConsumer struct {
	log      *zap.Logger
	treasury *treasury.Client
}

// NewTreasuryTaxEventsConsumer creates a new consumer. treasuryClient may be nil (caching
// disabled); the consumer then acks-and-skips so it never blocks the stream.
func NewTreasuryTaxEventsConsumer(log *zap.Logger, treasuryClient *treasury.Client) *TreasuryTaxEventsConsumer {
	return &TreasuryTaxEventsConsumer{
		log:      log.Named("consumers.treasury_tax_events"),
		treasury: treasuryClient,
	}
}

// Start subscribes to treasury.tax.code_updated via a JetStream durable consumer.
func (c *TreasuryTaxEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "treasury" stream exists (it's created by treasury-api, but may not exist yet)
	_, err := js.StreamInfo("treasury")
	if err != nil {
		c.log.Info("treasury stream not found, creating it for consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "treasury",
			Subjects:  []string{"treasury.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("treasury tax events: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		"treasury.tax.code_updated",
		treasuryTaxDurableConsumer,
		c.handleMessage,
		nats.Durable(treasuryTaxDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(treasuryTaxAckWait),
		nats.MaxDeliver(treasuryTaxMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("treasury tax events consumer started", zap.String("durable", treasuryTaxDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *TreasuryTaxEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	// The payload is a shared-events Event JSON; extract the inner payload fields.
	var envelope struct {
		Payload   treasuryTaxPayload `json:"payload"`
		EventType string             `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("treasury tax events: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("treasury tax events: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack() // don't retry malformed messages
		return
	}

	// Nil-safe: when treasury client / caching is disabled there is nothing to invalidate.
	if c.treasury == nil {
		c.log.Debug("treasury tax events: no treasury client — nothing to invalidate",
			zap.String("tenant_id", tenantID.String()))
		_ = msg.Ack()
		return
	}

	c.treasury.InvalidateTaxCache(ctx, tenantID)
	c.log.Info("treasury tax cache invalidated on tax-code change",
		zap.String("tenant_id", tenantID.String()),
		zap.String("code", envelope.Payload.Code),
		zap.String("action", envelope.Payload.Action),
	)

	_ = msg.Ack()
}
