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
	"github.com/bengobox/inventory-service/internal/modules/items"
)

const (
	etimsItemRegisteredDurableConsumer = "inventory-etims-item-registered-writeback"
	etimsItemRegisteredAckWait         = 30 * time.Second
	etimsItemRegisteredMaxDeliver      = 5
)

// etimsItemRegisteredPayload is the inner payload of a treasury.etims.item_registered event.
type etimsItemRegisteredPayload struct {
	TenantID     string `json:"tenant_id"`
	SourceItemID string `json:"source_item_id"` // inventory item UUID (primary join key)
	SKU          string `json:"sku"`            // fallback join key
	ItemCd       string `json:"item_cd"`
	ItemClsCd    string `json:"item_cls_cd"`
	PkgUnitCd    string `json:"pkg_unit_cd"`
	QtyUnitCd    string `json:"qty_unit_cd"`
	TaxTyCd      string `json:"tax_ty_cd"`
}

// EtimsItemRegisteredConsumer mirrors the KRA-assigned eTIMS codes (classification, package/
// quantity units) back onto the inventory item after treasury-api registers it in the eTIMS item
// master. This closes the loop so the inventory Edit-Item form shows an item's real synced eTIMS
// values instead of the tenant/system defaults. The item master is inventory's; the registration
// codes are minted in treasury — this consumer is the reverse projection of the forward
// inventory.item.created/updated → treasury RegisterEtimsItem flow.
type EtimsItemRegisteredConsumer struct {
	log      *zap.Logger
	itemsSvc *items.Service
}

// NewEtimsItemRegisteredConsumer creates the write-back consumer. itemsSvc must be non-nil.
func NewEtimsItemRegisteredConsumer(log *zap.Logger, itemsSvc *items.Service) *EtimsItemRegisteredConsumer {
	return &EtimsItemRegisteredConsumer{
		log:      log.Named("consumers.etims_item_registered"),
		itemsSvc: itemsSvc,
	}
}

// Start subscribes to treasury.etims.item_registered via a JetStream durable consumer.
func (c *EtimsItemRegisteredConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "treasury" stream exists (created by treasury-api, but may not exist yet).
	if _, err := js.StreamInfo("treasury"); err != nil {
		c.log.Info("treasury stream not found, creating it for consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "treasury",
			Subjects:  []string{"treasury.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("etims item registered: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		"treasury.etims.item_registered",
		etimsItemRegisteredDurableConsumer,
		c.handleMessage,
		nats.Durable(etimsItemRegisteredDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(etimsItemRegisteredAckWait),
		nats.MaxDeliver(etimsItemRegisteredMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("etims item-registered write-back consumer started", zap.String("durable", etimsItemRegisteredDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *EtimsItemRegisteredConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var envelope struct {
		Payload   etimsItemRegisteredPayload `json:"payload"`
		EventType string                     `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("etims item registered: unmarshal failed", zap.Error(err))
		_ = msg.Ack() // malformed — don't retry
		return
	}
	p := envelope.Payload

	tenantID, err := uuid.Parse(p.TenantID)
	if err != nil {
		c.log.Warn("etims item registered: invalid tenant_id", zap.String("raw", p.TenantID))
		_ = msg.Ack()
		return
	}

	var itemID *uuid.UUID
	if p.SourceItemID != "" {
		if id, perr := uuid.Parse(p.SourceItemID); perr == nil {
			itemID = &id
		}
	}
	if itemID == nil && p.SKU == "" {
		c.log.Debug("etims item registered: no item identifier — skipping",
			zap.String("tenant_id", tenantID.String()))
		_ = msg.Ack()
		return
	}

	if err := c.itemsSvc.SetEtimsRegistration(ctx, tenantID, itemID, p.SKU, items.EtimsRegistration{
		ItemCd:    p.ItemCd,
		ItemClsCd: p.ItemClsCd,
		PkgUnitCd: p.PkgUnitCd,
		QtyUnitCd: p.QtyUnitCd,
	}); err != nil {
		c.log.Warn("etims item registered: write-back failed — will retry",
			zap.String("tenant_id", tenantID.String()),
			zap.String("sku", p.SKU),
			zap.Error(err))
		_ = msg.Nak()
		return
	}

	c.log.Info("etims registration codes mirrored to inventory item",
		zap.String("tenant_id", tenantID.String()),
		zap.String("sku", p.SKU),
		zap.String("item_cls_cd", p.ItemClsCd))
	_ = msg.Ack()
}
