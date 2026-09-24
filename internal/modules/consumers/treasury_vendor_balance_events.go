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
	"github.com/bengobox/inventory-service/internal/modules/vendorbalances"
)

const (
	treasuryVendorBalanceDurableConsumer = "inventory-treasury-vendor-balance-updated"
	treasuryVendorBalanceAckWait         = 30 * time.Second
	treasuryVendorBalanceMaxDeliver      = 3
)

// treasuryVendorBalancePayload is the inner payload of treasury.vendor.balance_updated.
type treasuryVendorBalancePayload struct {
	VendorBalanceID    string `json:"vendor_balance_id"`
	VendorID           string `json:"vendor_id"`
	VendorIdentifier   string `json:"vendor_identifier"`
	VendorName         string `json:"vendor_name"`
	BalanceOwed        string `json:"balance_owed"`
	OutstandingPayable string `json:"outstanding_payable"`
	Currency           string `json:"currency"`
}

// TreasuryVendorBalanceEventsConsumer keeps VendorBalanceCache fresh — closes the one-way sync
// gap where a bill payment / vendor refund recorded directly in treasury-ui never reached
// inventory-api at all (treasury owns AP balances; inventory owns the Supplier master, per
// treasury-vendor-master-via-inventory — this cache is purely a read-side mirror for whichever
// procurement surface needs "has this supplier been paid" without an S2S round-trip). Durable +
// idempotent: the cache write is a pure upsert (last-write-wins on synced_at), safe to redeliver.
type TreasuryVendorBalanceEventsConsumer struct {
	log *zap.Logger
	db  *ent.Client
}

// NewTreasuryVendorBalanceEventsConsumer creates a new consumer.
func NewTreasuryVendorBalanceEventsConsumer(log *zap.Logger, db *ent.Client) *TreasuryVendorBalanceEventsConsumer {
	return &TreasuryVendorBalanceEventsConsumer{
		log: log.Named("consumers.treasury_vendor_balance_events"),
		db:  db,
	}
}

// Start subscribes to treasury.vendor.balance_updated via a JetStream durable consumer.
func (c *TreasuryVendorBalanceEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
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
			return fmt.Errorf("treasury vendor balance events: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log, js, "treasury", "treasury.vendor.balance_updated", treasuryVendorBalanceDurableConsumer,
		c.handleMessage,
		nats.Durable(treasuryVendorBalanceDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(treasuryVendorBalanceAckWait),
		nats.MaxDeliver(treasuryVendorBalanceMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("treasury vendor balance events consumer started", zap.String("durable", treasuryVendorBalanceDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *TreasuryVendorBalanceEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var envelope struct {
		TenantID string                       `json:"tenant_id"`
		Payload  treasuryVendorBalancePayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("treasury vendor balance events: unmarshal failed", zap.Error(err))
		_ = msg.Ack() // malformed message — never retry
		return
	}

	tenantID, err := uuid.Parse(envelope.TenantID)
	if err != nil {
		c.log.Warn("treasury vendor balance events: invalid tenant_id", zap.String("raw", envelope.TenantID))
		_ = msg.Ack()
		return
	}

	var vendorID *uuid.UUID
	if envelope.Payload.VendorID != "" {
		if id, perr := uuid.Parse(envelope.Payload.VendorID); perr == nil {
			vendorID = &id
		}
	}
	if vendorID == nil && envelope.Payload.VendorIdentifier == "" {
		_ = msg.Ack() // nothing to key the cache row on
		return
	}

	err = vendorbalances.Upsert(ctx, c.db, tenantID, vendorbalances.Entry{
		VendorID:           vendorID,
		VendorIdentifier:   envelope.Payload.VendorIdentifier,
		VendorName:         envelope.Payload.VendorName,
		BalanceOwed:        envelope.Payload.BalanceOwed,
		OutstandingPayable: envelope.Payload.OutstandingPayable,
		Currency:           envelope.Payload.Currency,
	})
	if err != nil {
		// This is a read-then-write TOCTOU: two redeliveries of the same balance-updated event
		// (AckWait=30s/MaxDeliver=3) racing the First() lookup above could both miss the existing
		// row and both attempt Create(). For the vendor_id-keyed path that's caught by the
		// unique(tenant_id, vendor_id) index -- treat that as idempotent success (the other
		// racer's write already landed the same data) rather than Nak-ing into a pointless retry
		// storm. There is no equivalent constraint for the vendor_identifier-only fallback path
		// (used when treasury hasn't resolved an inventory vendor UUID yet), so a duplicate row is
		// still possible there -- accepted as a low-severity residual risk since this cache is a
		// pure best-effort read-side mirror, never a source of financial truth.
		if ent.IsConstraintError(err) {
			c.log.Info("vendor balance cache: row already written by a concurrent redelivery, skipping (idempotent)",
				zap.String("tenant_id", tenantID.String()))
			_ = msg.Ack()
			return
		}
		c.log.Error("treasury vendor balance events: upsert cache row", zap.Error(err))
		_ = msg.Nak()
		return
	}

	c.log.Info("vendor balance cache synced from treasury",
		zap.String("tenant_id", tenantID.String()),
		zap.String("vendor_name", envelope.Payload.VendorName),
		zap.String("balance_owed", envelope.Payload.BalanceOwed))
	_ = msg.Ack()
}
