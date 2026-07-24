package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventoryserial"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	posSalesDurableConsumer = "inventory-pos-sales"
	posSalesAckWait         = 30 * time.Second
	posSalesMaxDeliver      = 5
)

// posSaleItem is a line item from the POS sale event.
type posSaleItem struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
	UOMCode  string  `json:"uom_code"`
	// SkipInventory is set by pos-api for a pharmacy-checkout order whose stock was already
	// committed at Dispense time (ConsumeReservation) — deducting again here would double-count
	// the same drop. See pos-api's payments/service.go publishSaleFinalized.
	SkipInventory bool `json:"skip_inventory,omitempty"`
	// Serials are the specific unit serial numbers sold on this line (serial-tracked items).
	// Each is flipped from "available" to "sold" in the InventorySerial registry.
	Serials []string `json:"serials,omitempty"`
	// Modifiers is the field pos-api currently emits (pos.sale.finalized items[].modifiers).
	Modifiers []posModifierOption `json:"modifiers,omitempty"`
	// ModifierOptions is a legacy alias kept for backward compatibility.
	ModifierOptions []posModifierOption `json:"modifier_options,omitempty"`
}

// posModifierOption is a selected modifier on a sale line. pos-api sends the inventory
// modifier-option id (preferred); some older payloads carry a direct sku.
type posModifierOption struct {
	SKU                       string  `json:"sku"`
	InventoryModifierOptionID string  `json:"inventory_modifier_option_id"`
	Quantity                  float64 `json:"quantity"`
}

// POSSaleEventsConsumer consumes pos.sale.finalized events to record stock consumption.
type POSSaleEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	orm      *ent.Client
	// hasFeature gates cross-service stock sync by subscription entitlement. When set,
	// POS sales only consume inventory stock for tenants entitled to basic_inventory_access
	// (the cross-service inventory-access feature auto-injected into POS plans — the same
	// code the POS→inventory direction gates on). Nil → no gating (fail open).
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewPOSSaleEventsConsumer creates a new POS sale events consumer.
func NewPOSSaleEventsConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *POSSaleEventsConsumer {
	return &POSSaleEventsConsumer{
		log:      log.Named("consumers.pos_sale_events"),
		stockSvc: stockSvc,
		orm:      orm,
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate stock sync.
func (c *POSSaleEventsConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may have POS sales consume its inventory stock.
// Fails open when no gate is wired.
func (c *POSSaleEventsConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
}

// Start begins listening for POS sale events via JetStream durable consumer.
func (c *POSSaleEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "pos" stream exists (it's created by pos-api, but may not exist yet)
	_, err := js.StreamInfo("pos")
	if err != nil {
		c.log.Info("pos stream not found, creating it for consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "pos",
			Subjects:  []string{"pos.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("pos sale events: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"pos",
		"pos.sale.finalized",
		posSalesDurableConsumer,
		c.handleMessage,
		nats.Durable(posSalesDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(posSalesAckWait),
		nats.MaxDeliver(posSalesMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("pos sale events consumer started", zap.String("durable", posSalesDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *POSSaleEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var envelope struct {
		Payload struct {
			TenantID    string        `json:"tenant_id"`
			TenantSlug  string        `json:"tenant_slug"`
			OrderID     string        `json:"order_id"`
			OutletID    string        `json:"outlet_id"`
			WarehouseID string        `json:"warehouse_id"`
			Items       []posSaleItem `json:"items"`
		} `json:"payload"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("pos sale events: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("pos sale events: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack() // don't retry malformed messages
		return
	}
	orderID, err := uuid.Parse(envelope.Payload.OrderID)
	if err != nil {
		c.log.Warn("pos sale events: invalid order_id", zap.String("raw", envelope.Payload.OrderID))
		_ = msg.Ack()
		return
	}

	// Gate cross-service stock sync by subscription entitlement (fails open if no gate
	// wired / subscriptions-api down). Ack + skip when the tenant lacks basic_inventory_access.
	if !c.entitled(ctx, tenantID) {
		c.log.Debug("pos sale events: tenant lacks basic_inventory_access — skipping stock consumption",
			zap.String("tenant_id", tenantID.String()), zap.String("order_id", orderID.String()))
		_ = msg.Ack()
		return
	}

	var warehouseID uuid.UUID
	if envelope.Payload.WarehouseID != "" {
		warehouseID, err = uuid.Parse(envelope.Payload.WarehouseID)
		if err != nil {
			c.log.Warn("pos sale events: invalid warehouse_id", zap.String("raw", envelope.Payload.WarehouseID))
			// Continue with zero UUID (will resolve to the outlet's / default warehouse)
		}
	}

	// The SELLING outlet scopes the warehouse when no explicit warehouse_id is carried: the sale
	// must deduct from the outlet's OWN warehouse, not the tenant default. pos-api emits outlet_id
	// on every pos.sale.finalized event; an absent/invalid value simply falls back to the default.
	var outletID uuid.UUID
	if envelope.Payload.OutletID != "" {
		if outletID, err = uuid.Parse(envelope.Payload.OutletID); err != nil {
			c.log.Warn("pos sale events: invalid outlet_id", zap.String("raw", envelope.Payload.OutletID))
			outletID = uuid.Nil
		}
	}

	// Drop skip_inventory items (pharmacy-checkout lines already deducted at Dispense time via
	// ConsumeReservation) before consumption — see posSaleItem.SkipInventory.
	saleItems := envelope.Payload.Items
	consumable := saleItems[:0]
	for _, it := range saleItems {
		if it.SkipInventory {
			continue
		}
		consumable = append(consumable, it)
	}

	if err := c.handleSaleFinalized(ctx, tenantID, orderID, warehouseID, outletID, consumable); err != nil {
		c.log.Error("pos sale events: handle sale finalized failed",
			zap.Error(err),
			zap.String("order_id", orderID.String()),
			zap.String("tenant_id", tenantID.String()),
		)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}

// handleSaleFinalized processes a finalized POS sale by consuming stock.
// All BOM explosion, unit conversion, modifier draws and non-depletion policy live in
// stock.Service.RecordConsumption (the ONE shared deduction path also used by the S2S
// consumption/reservation endpoints) — the consumer only validates SKUs (so one unknown
// line never NAK-loops the event) and flips sold serials.
func (c *POSSaleEventsConsumer) handleSaleFinalized(ctx context.Context, tenantID, orderID, warehouseID, outletID uuid.UUID, saleItems []posSaleItem) error {
	var consumptionItems []stock.ConsumptionItem

	for _, si := range saleItems {
		// Look up the item to determine its type. A variant SKU is NOT on Item (it lives on
		// ItemVariant with its own sku) — ResolveItemBySKU falls back to ItemVariant→parent
		// so variant sales deduct the PARENT item's stock/BOM instead of silently skipping.
		itm, err := c.stockSvc.ResolveItemBySKU(ctx, tenantID, si.SKU)
		if err != nil {
			if ent.IsNotFound(err) {
				c.log.Warn("pos sale events: item not found, skipping",
					zap.String("sku", si.SKU),
					zap.String("tenant_id", tenantID.String()),
				)
				continue
			}
			return fmt.Errorf("query item sku=%s: %w", si.SKU, err)
		}

		// Variants share the parent's recipe + balance, so consume against the PARENT
		// item's SKU (itm.Sku). Modifiers ride the same line — the service resolves the
		// option→SKU mapping and scales one application per sold unit.
		mods := append(append([]posModifierOption{}, si.Modifiers...), si.ModifierOptions...)
		modifierLines := make([]stock.ModifierLine, 0, len(mods))
		for _, mod := range mods {
			modifierLines = append(modifierLines, stock.ModifierLine{
				SKU:                       mod.SKU,
				InventoryModifierOptionID: mod.InventoryModifierOptionID,
				Quantity:                  mod.Quantity,
			})
		}
		consumptionItems = append(consumptionItems, stock.ConsumptionItem{
			SKU:       itm.Sku,
			Quantity:  si.Quantity,
			UOM:       si.UOMCode,
			Modifiers: modifierLines,
		})

		// Flip any sold serials to "sold" in the per-unit registry (best-effort, in addition to
		// the aggregate quantity decrement above). Only meaningful for serial-tracked items.
		if itm.TrackSerialNumbers && len(si.Serials) > 0 {
			c.markSerialsSold(ctx, tenantID, itm.ID, si.Serials, orderID)
		}
	}

	if len(consumptionItems) == 0 {
		c.log.Info("pos sale events: no items to consume",
			zap.String("order_id", orderID.String()),
		)
		return nil
	}

	// Record consumption using existing stock service. OutletID scopes the deduction to the
	// selling outlet's own warehouse when no explicit warehouse_id was carried.
	_, err := c.stockSvc.RecordConsumption(ctx, tenantID, stock.ConsumptionRequest{
		TenantID:       tenantID,
		OrderID:        orderID,
		WarehouseID:    warehouseID,
		OutletID:       outletID,
		Items:          consumptionItems,
		Reason:         "pos_sale",
		IdempotencyKey: fmt.Sprintf("pos-sale-%s", orderID.String()),
	})
	if err != nil {
		return fmt.Errorf("record consumption for order %s: %w", orderID, err)
	}

	c.log.Info("pos sale consumption recorded",
		zap.String("order_id", orderID.String()),
		zap.Int("consumption_items", len(consumptionItems)),
	)
	return nil
}

// markSerialsSold transitions the given serials from "available" to "sold" for a serial-tracked
// item. Best-effort: a serial that's missing or already non-available is logged, not fatal, so a
// serial mismatch never blocks the sale's stock consumption.
func (c *POSSaleEventsConsumer) markSerialsSold(ctx context.Context, tenantID, itemID uuid.UUID, serials []string, orderID uuid.UUID) {
	now := time.Now()
	for _, sn := range serials {
		sn = strings.TrimSpace(sn)
		if sn == "" {
			continue
		}
		n, err := c.orm.InventorySerial.Update().
			Where(
				inventoryserial.TenantID(tenantID),
				inventoryserial.ItemID(itemID),
				inventoryserial.SerialNumber(sn),
				inventoryserial.StatusEQ(inventoryserial.StatusAvailable),
			).
			SetStatus(inventoryserial.StatusSold).
			SetSoldAt(now).
			SetPosOrderLineID(orderID.String()).
			ClearWarehouseID().
			Save(ctx)
		if err != nil {
			c.log.Warn("mark serial sold failed", zap.String("serial", sn), zap.Error(err))
			continue
		}
		if n == 0 {
			c.log.Warn("serial not available to sell (missing or already sold)",
				zap.String("serial", sn), zap.String("item_id", itemID.String()))
		}
	}
}

// BOM explosion, modifier resolution, unit conversion and non-depletion policy are
// deliberately NOT duplicated here — stock.Service.RecordConsumption owns them (see
// internal/modules/stock/bom.go), keeping the event and S2S paths byte-identical.
