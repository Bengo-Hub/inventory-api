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
	entadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	// deliveryNoteDispatchedDurableConsumer is a NEW distinct durable/queue name so this
	// consumer load-balances across inventory-api replicas independently of the procure-to-order
	// (quotation_accepted) consumer that shares the same "treasury" stream.
	deliveryNoteDispatchedDurableConsumer = "inventory-delivery-note-goods-issue"
	deliveryNoteDispatchedAckWait         = 30 * time.Second
	deliveryNoteDispatchedMaxDeliver      = 3

	// deliveryNoteDispatchedSubject is the subject treasury-api publishes on the "treasury"
	// JetStream stream when a delivery note is dispatched (goods physically leave the warehouse).
	deliveryNoteDispatchedSubject = "treasury.delivery_note.dispatched"
)

// deliveryNoteLine is one dispatched line on the delivery note. item_id is an sku-or-uuid string
// (resolve by id first, then by sku); quantity arrives as a JSON string decimal (decimal.Decimal
// marshals to a string) OR a number, so it is decoded flexibly via flexFloat.
type deliveryNoteLine struct {
	ItemID      string    `json:"item_id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	Quantity    flexFloat `json:"quantity"`
}

// deliveryNoteDispatchedPayload is the inner payload of the delivery-note-dispatched event.
type deliveryNoteDispatchedPayload struct {
	TenantID           string `json:"tenant_id"`
	DeliveryNoteID     string `json:"delivery_note_id"`
	DeliveryNoteNumber string `json:"delivery_note_number"`
	SourceInvoiceID    string `json:"source_invoice_id"`
	// OutletID is the treasury outlet/branch the delivery note dispatched from. "" for a
	// tenant-wide note. Used to pick the issuing warehouse (branch-aware, default fallback).
	OutletID     string             `json:"outlet_id"`
	CustomerName string             `json:"customer_name"`
	Lines        []deliveryNoteLine `json:"lines"`
}

// DeliveryNoteDispatchedConsumer implements goods-issue: when a treasury delivery note is
// DISPATCHED, it deducts the dispatched quantities from the issuing warehouse's stock via the
// canonical stock-adjustment path (auditable StockAdjustment + balance decrement + lot drawdown).
// Idempotent on delivery_note_id.
type DeliveryNoteDispatchedConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	orm      *ent.Client
	// hasFeature gates goods-issue by subscription entitlement. Fail-open when nil.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewDeliveryNoteDispatchedConsumer creates a new goods-issue consumer.
func NewDeliveryNoteDispatchedConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *DeliveryNoteDispatchedConsumer {
	return &DeliveryNoteDispatchedConsumer{
		log:      log.Named("consumers.delivery_note_dispatched"),
		stockSvc: stockSvc,
		orm:      orm,
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate goods-issue.
func (c *DeliveryNoteDispatchedConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may have a dispatched delivery note deduct its inventory
// stock. Fails open when no gate is wired (subscriptions-api down / not configured).
func (c *DeliveryNoteDispatchedConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
}

// Start subscribes to the delivery-note-dispatched subject via a JetStream durable queue consumer.
func (c *DeliveryNoteDispatchedConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "treasury" stream exists (owned by treasury-api, but may not exist yet at boot).
	_, err := js.StreamInfo("treasury")
	if err != nil {
		c.log.Info("treasury stream not found, creating it for goods-issue consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "treasury",
			Subjects:  []string{"treasury.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("delivery note dispatched: ensure stream: %w", err)
		}
	}

	// Durable QUEUE consumer so BOTH inventory-api replicas share the consumer (load-balanced,
	// multi-replica safe) instead of one replica binding and the other looping on
	// "consumer is already bound". The durable name is reused as the deliver-group name.
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		deliveryNoteDispatchedSubject,
		deliveryNoteDispatchedDurableConsumer,
		c.handleMessage,
		nats.Durable(deliveryNoteDispatchedDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(deliveryNoteDispatchedAckWait),
		nats.MaxDeliver(deliveryNoteDispatchedMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("delivery note dispatched (goods-issue) consumer started",
		zap.String("durable", deliveryNoteDispatchedDurableConsumer),
		zap.String("subject", deliveryNoteDispatchedSubject))

	<-ctx.Done()
	return nil
}

func (c *DeliveryNoteDispatchedConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	// shared-events envelope: tenant_id is carried at the top level; payload holds the rest.
	var envelope struct {
		TenantID  string                        `json:"tenant_id"`
		EventType string                        `json:"event_type"`
		Payload   deliveryNoteDispatchedPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("delivery note dispatched: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	p := envelope.Payload
	// Tenant id lives on the envelope (shared-events moves it out of payload); fall back to the
	// payload copy for resilience against contract drift.
	tenantRaw := envelope.TenantID
	if tenantRaw == "" {
		tenantRaw = p.TenantID
	}
	tenantID, err := uuid.Parse(tenantRaw)
	if err != nil {
		c.log.Warn("delivery note dispatched: invalid tenant_id", zap.String("raw", tenantRaw))
		_ = msg.Ack() // malformed — don't retry
		return
	}
	deliveryNoteID, err := uuid.Parse(p.DeliveryNoteID)
	if err != nil {
		c.log.Warn("delivery note dispatched: invalid delivery_note_id", zap.String("raw", p.DeliveryNoteID))
		_ = msg.Ack()
		return
	}

	// Gate goods-issue by subscription entitlement (fails open if no gate wired).
	if !c.entitled(ctx, tenantID) {
		c.log.Debug("delivery note dispatched: tenant lacks basic_inventory_access — skipping goods-issue",
			zap.String("tenant_id", tenantID.String()), zap.String("delivery_note_id", deliveryNoteID.String()))
		_ = msg.Ack()
		return
	}

	if err := c.handleDeliveryNoteDispatched(ctx, tenantID, deliveryNoteID, p); err != nil {
		c.log.Error("delivery note dispatched: goods-issue failed",
			zap.Error(err),
			zap.String("delivery_note_id", deliveryNoteID.String()),
			zap.String("tenant_id", tenantID.String()),
		)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}

// goodsIssueReference is the StockAdjustment.reference written for every line of a dispatched
// delivery note. It both records traceability (which dispatch caused the stock-out) AND serves as
// the idempotency key: the handler skips processing if any adjustment already carries this
// reference for the tenant (handles JetStream redelivery — process a dispatch exactly once).
func goodsIssueReference(deliveryNoteID uuid.UUID) string {
	return fmt.Sprintf("delivery-note-dispatch-%s", deliveryNoteID.String())
}

// handleDeliveryNoteDispatched deducts each dispatched line's quantity from the issuing warehouse
// using the canonical stock-out path (stock.Service.AdjustStock with a negative adjustment), which
// decrements on-hand/available, draws down lots, writes a StockAdjustment audit row, and emits
// stock.updated / stock.out events. Idempotent on delivery_note_id (via the adjustment reference).
// Unresolved items / non-positive quantities are skipped+warned, never failing the whole batch.
func (c *DeliveryNoteDispatchedConsumer) handleDeliveryNoteDispatched(ctx context.Context, tenantID, deliveryNoteID uuid.UUID, p deliveryNoteDispatchedPayload) error {
	reference := goodsIssueReference(deliveryNoteID)

	// Idempotency: skip if any stock adjustment already references this dispatch (redelivery-safe).
	exists, err := c.orm.StockAdjustment.Query().
		Where(entadj.TenantID(tenantID), entadj.Reference(reference)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check existing goods-issue adjustment: %w", err)
	}
	if exists {
		c.log.Info("delivery note dispatched: goods-issue already applied — skipping (idempotent)",
			zap.String("delivery_note_id", deliveryNoteID.String()),
			zap.String("delivery_note_number", p.DeliveryNoteNumber))
		return nil
	}

	// Branch-aware: resolve the issuing warehouse from the event's outlet_id (active warehouse
	// whose outlet_id matches, for this tenant); fall back to the tenant's default active
	// warehouse when the event has no outlet or no warehouse matches.
	warehouseID := c.resolveWarehouse(ctx, tenantID, p.OutletID)
	if warehouseID == uuid.Nil {
		c.log.Warn("delivery note dispatched: no issuing warehouse resolved — skipping goods-issue",
			zap.String("delivery_note_id", deliveryNoteID.String()),
			zap.String("outlet_id", p.OutletID))
		return nil
	}

	notes := fmt.Sprintf("Goods issue: auto-deducted on dispatch of delivery note %s (customer: %s).",
		p.DeliveryNoteNumber, p.CustomerName)

	issued := 0
	for _, line := range p.Lines {
		// Resolve the line to an inventory item (by item_id first, then sku). AdjustStock keys off
		// the item SKU, so we resolve to the item to obtain its canonical SKU.
		itm := c.resolveItem(ctx, tenantID, line.ItemID, line.SKU)
		if itm == nil {
			c.log.Warn("delivery note dispatched: could not resolve inventory item for line — skipping",
				zap.String("item_id", line.ItemID),
				zap.String("sku", line.SKU),
				zap.String("delivery_note_id", deliveryNoteID.String()))
			continue
		}

		qty := float64(line.Quantity)
		if qty <= 0 {
			c.log.Warn("delivery note dispatched: non-positive quantity for line — skipping",
				zap.String("sku", itm.Sku),
				zap.Float64("quantity", qty),
				zap.String("delivery_note_id", deliveryNoteID.String()))
			continue
		}

		// Canonical stock-out: a NEGATIVE adjustment decrements on-hand/available, draws down
		// lots (FIFO/LIFO/FEFO per tenant costing method), writes the StockAdjustment audit row,
		// and publishes stock.updated / stock.out. reason=transfer_out (goods leaving on dispatch);
		// reference ties every line to this delivery note (and is the idempotency key).
		if _, aerr := c.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU:         itm.Sku,
			Adjustment:  -qty,
			Reason:      "transfer_out",
			Reference:   reference,
			Notes:       notes,
			WarehouseID: warehouseID,
		}); aerr != nil {
			// Skip+warn a single bad line (e.g. no balance row) — never fail the whole batch.
			c.log.Warn("delivery note dispatched: failed to deduct stock for line — skipping",
				zap.Error(aerr),
				zap.String("sku", itm.Sku),
				zap.Float64("quantity", qty),
				zap.String("delivery_note_id", deliveryNoteID.String()))
			continue
		}
		issued++
	}

	c.log.Info("goods-issue applied from dispatched delivery note",
		zap.String("delivery_note_id", deliveryNoteID.String()),
		zap.String("delivery_note_number", p.DeliveryNoteNumber),
		zap.String("warehouse_id", warehouseID.String()),
		zap.String("outlet_id", p.OutletID),
		zap.Int("lines_issued", issued),
		zap.Int("lines_total", len(p.Lines)),
	)
	return nil
}

// resolveWarehouse picks the issuing warehouse for goods-issue (branch-aware), mirroring the
// quotation (procure-to-order) consumer's outlet_id → default-fallback logic:
//   - if the event carries a valid outlet_id, the active warehouse whose outlet_id matches for
//     this tenant;
//   - otherwise (no outlet, unparseable, or no match) the tenant's default active warehouse.
//
// Returns uuid.Nil when nothing matches (the dispatch is then skipped — no warehouse to issue from).
func (c *DeliveryNoteDispatchedConsumer) resolveWarehouse(ctx context.Context, tenantID uuid.UUID, outletIDRaw string) uuid.UUID {
	if outletIDRaw != "" {
		if outletID, err := uuid.Parse(outletIDRaw); err == nil {
			if wh, werr := c.orm.Warehouse.Query().
				Where(entwh.TenantID(tenantID), entwh.OutletID(outletID), entwh.IsActive(true)).
				First(ctx); werr == nil {
				return wh.ID
			} else if !ent.IsNotFound(werr) {
				c.log.Warn("delivery note dispatched: outlet warehouse lookup failed", zap.Error(werr),
					zap.String("outlet_id", outletIDRaw))
			} else {
				c.log.Info("delivery note dispatched: no warehouse for outlet — falling back to default",
					zap.String("outlet_id", outletIDRaw))
			}
		} else {
			c.log.Warn("delivery note dispatched: invalid outlet_id — falling back to default warehouse",
				zap.String("outlet_id", outletIDRaw))
		}
	}
	// Fallback: tenant's default active warehouse.
	if wh, werr := c.orm.Warehouse.Query().
		Where(entwh.TenantID(tenantID), entwh.IsDefault(true), entwh.IsActive(true)).
		First(ctx); werr == nil {
		return wh.ID
	} else if !ent.IsNotFound(werr) {
		c.log.Warn("delivery note dispatched: default warehouse lookup failed", zap.Error(werr))
	}
	return uuid.Nil
}

// resolveItem resolves a delivery-note line to an inventory item by id first, then by sku within
// the tenant (mirrors the quotation consumer's resolveItem). Returns nil when neither resolves
// (the line is then skipped, never blocking the batch).
func (c *DeliveryNoteDispatchedConsumer) resolveItem(ctx context.Context, tenantID uuid.UUID, itemIDRaw, sku string) *ent.Item {
	if itemIDRaw != "" {
		if id, err := uuid.Parse(itemIDRaw); err == nil {
			if itm, qerr := c.orm.Item.Query().
				Where(entitem.ID(id), entitem.TenantID(tenantID)).
				Only(ctx); qerr == nil {
				return itm
			}
		}
	}
	if sku != "" {
		if itm, qerr := c.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(sku)).
			Only(ctx); qerr == nil {
			return itm
		}
	}
	return nil
}
