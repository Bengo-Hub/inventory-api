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
	"github.com/bengobox/inventory-service/internal/modules/bulkjobs"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
	"github.com/bengobox/inventory-service/internal/modules/tenantconfig"
)

const (
	treasuryTaxDurableConsumer = "inventory-treasury-tax-cache-invalidate"
	treasuryVATDurableConsumer = "inventory-treasury-vat-status"
	treasuryTaxAckWait         = 30 * time.Second
	treasuryTaxMaxDeliver      = 3
)

// treasuryTaxPayload is the inner payload of a treasury.tax.code_updated event.
type treasuryTaxPayload struct {
	TenantID string `json:"tenant_id"`
	Code     string `json:"code"`
	Action   string `json:"action"`
}

// treasuryVATPayload is the inner payload of a treasury.tax.vat_status_changed event.
type treasuryVATPayload struct {
	TenantID  string `json:"tenant_id"`
	VATActive bool   `json:"vat_active"`
}

// TreasuryTaxEventsConsumer listens for treasury tax-code changes and invalidates the
// locally cached treasury tax data so rate changes propagate immediately instead of
// waiting for the ~10-minute cache TTL.
//
// It also follows treasury.tax.vat_status_changed: when a tenant starts charging VAT the catalog's
// automatic tax codes move from band D (Non-VAT) to band B (VAT 16%), and back when it stops.
// Codes a person set on an item are left alone.
type TreasuryTaxEventsConsumer struct {
	log      *zap.Logger
	treasury *treasury.Client
	items    *items.Service
	jobs     *bulkjobs.Service
}

// SetCatalogTaxJobs wires the items service and bulk-job runner the VAT status handler uses.
// Without them the VAT event only refreshes the cached tax data.
func (c *TreasuryTaxEventsConsumer) SetCatalogTaxJobs(itemsSvc *items.Service, jobs *bulkjobs.Service) {
	c.items, c.jobs = itemsSvc, jobs
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
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		"treasury.tax.vat_status_changed",
		treasuryVATDurableConsumer,
		c.handleVATStatus,
		nats.Durable(treasuryVATDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(treasuryTaxAckWait),
		nats.MaxDeliver(treasuryTaxMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("treasury tax events consumer started",
		zap.String("durable", treasuryTaxDurableConsumer), zap.String("vat_durable", treasuryVATDurableConsumer))

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

// handleVATStatus re-bands the tenant's automatic item tax codes after a VAT switch. Treasury's
// cached answer is dropped first so the job plans against the new status. The job itself runs in
// the background; the message is acked once it is queued. A replay is harmless: the plan finds
// nothing left to move.
func (c *TreasuryTaxEventsConsumer) handleVATStatus(msg *nats.Msg) {
	ctx := context.Background()
	var envelope struct {
		Payload treasuryVATPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("treasury vat status: unmarshal failed", zap.Error(err))
		_ = msg.Ack()
		return
	}
	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		_ = msg.Ack()
		return
	}
	if c.treasury != nil {
		c.treasury.InvalidateTaxCache(ctx, tenantID)
	}
	if c.items == nil {
		_ = msg.Ack()
		return
	}
	vat := envelope.Payload.VATActive
	if c.items.ResetBandDefaultOnVATChange(ctx, tenantID, vat) {
		tenantconfig.Invalidate(tenantID)
	}
	mode := items.TaxJobReconcile
	if !vat {
		mode = items.TaxJobVATOff
	}
	job, plan, err := c.items.QueueTenantTaxCodes(ctx, c.jobs, tenantID, mode, uuid.Nil)
	if err != nil {
		c.log.Warn("treasury vat status: tax-code job not queued", zap.String("tenant", tenantID.String()), zap.Error(err))
		_ = msg.Nak() // treasury may be briefly unreachable; retried up to MaxDeliver
		return
	}
	fields := []zap.Field{zap.String("tenant", tenantID.String()), zap.Bool("vat_active", vat), zap.Int("items", plan.Total)}
	if job != nil {
		fields = append(fields, zap.String("job_id", job.ID.String()))
	}
	c.log.Info("treasury vat status: catalog tax codes queued", fields...)
	_ = msg.Ack()
}
