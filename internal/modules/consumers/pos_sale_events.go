package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventoryserial"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
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
			// Continue with zero UUID (will resolve to default warehouse)
		}
	}

	if err := c.handleSaleFinalized(ctx, tenantID, orderID, warehouseID, envelope.Payload.Items); err != nil {
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
// For RECIPE items, it explodes the BOM and consumes ingredient stock.
// For other items, it records direct stock consumption.
func (c *POSSaleEventsConsumer) handleSaleFinalized(ctx context.Context, tenantID, orderID, warehouseID uuid.UUID, saleItems []posSaleItem) error {
	// Build the final list of consumption items after BOM explosion
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
		// Variants share the parent's recipe + balance, so explode/consume against the
		// PARENT item's SKU (itm.Sku), not the raw variant SKU.
		stockSKU := itm.Sku

		if itm.Type == item.TypeRECIPE {
			// BOM explosion: look up recipe by SKU, get ingredients, multiply by quantity
			ingredients, err := c.explodeBOM(ctx, tenantID, stockSKU, si.Quantity)
			if err != nil {
				c.log.Error("pos sale events: BOM explosion failed, falling back to direct consumption",
					zap.Error(err),
					zap.String("sku", stockSKU),
				)
				// Fall back to direct consumption of the recipe item itself
				consumptionItems = append(consumptionItems, stock.ConsumptionItem{
					SKU:      stockSKU,
					Quantity: si.Quantity,
				})
				continue
			}
			consumptionItems = append(consumptionItems, ingredients...)
		} else {
			// Direct consumption for non-RECIPE items
			consumptionItems = append(consumptionItems, stock.ConsumptionItem{
				SKU:      stockSKU,
				Quantity: si.Quantity,
			})
		}

		// Flip any sold serials to "sold" in the per-unit registry (best-effort, in addition to
		// the aggregate quantity decrement above). Only meaningful for serial-tracked items.
		if itm.TrackSerialNumbers && len(si.Serials) > 0 {
			c.markSerialsSold(ctx, tenantID, itm.ID, si.Serials, orderID)
		}

		// Add stock draw for selected modifiers. pos-api sends the inventory modifier-option
		// id; resolve it to the option's consumable SKU, then explode/deduct like any item
		// (one modifier application per sold unit → scale by line quantity).
		mods := append(append([]posModifierOption{}, si.Modifiers...), si.ModifierOptions...)
		for _, mod := range mods {
			sku := mod.SKU
			if sku == "" && mod.InventoryModifierOptionID != "" {
				if oid, perr := uuid.Parse(mod.InventoryModifierOptionID); perr == nil {
					if opt, oerr := c.orm.ModifierOption.Get(ctx, oid); oerr == nil {
						sku = opt.Sku
					}
				}
			}
			if sku == "" {
				continue
			}
			consumptionItems = append(consumptionItems, c.resolveConsumption(ctx, tenantID, sku, si.Quantity)...)
		}
	}

	if len(consumptionItems) == 0 {
		c.log.Info("pos sale events: no items to consume",
			zap.String("order_id", orderID.String()),
		)
		return nil
	}

	// Record consumption using existing stock service
	_, err := c.stockSvc.RecordConsumption(ctx, tenantID, stock.ConsumptionRequest{
		TenantID:       tenantID,
		OrderID:        orderID,
		WarehouseID:    warehouseID,
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

// resolveConsumption returns the stock-consumption items for a single SKU and quantity:
// a RECIPE explodes into its ingredient BOM; anything else (or unknown) consumes directly.
// Used for modifier-option draws so modifier ingredients are no longer leaked.
func (c *POSSaleEventsConsumer) resolveConsumption(ctx context.Context, tenantID uuid.UUID, sku string, qty float64) []stock.ConsumptionItem {
	// Resolve via Item-then-variant so a modifier option that points at a variant SKU
	// still deducts the parent item's stock/BOM. Deduct against the resolved parent SKU.
	itm, err := c.stockSvc.ResolveItemBySKU(ctx, tenantID, sku)
	if err != nil {
		return []stock.ConsumptionItem{{SKU: sku, Quantity: qty}}
	}
	if itm.Type == item.TypeRECIPE {
		if ings, e := c.explodeBOM(ctx, tenantID, itm.Sku, qty); e == nil {
			return ings
		}
	}
	return []stock.ConsumptionItem{{SKU: itm.Sku, Quantity: qty}}
}

// explodeBOM looks up a recipe by SKU and returns the ingredient consumption items
// multiplied by the sale quantity.
func (c *POSSaleEventsConsumer) explodeBOM(ctx context.Context, tenantID uuid.UUID, sku string, saleQty float64) ([]stock.ConsumptionItem, error) {
	r, err := c.orm.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(sku), recipe.IsActive(true)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipe not found for sku=%s: %w", sku, err)
	}

	if len(r.Edges.Ingredients) == 0 {
		return nil, fmt.Errorf("recipe sku=%s has no ingredients", sku)
	}

	// Calculate multiplier: saleQty / outputQty
	multiplier := saleQty / r.OutputQty

	items := make([]stock.ConsumptionItem, 0, len(r.Edges.Ingredients))
	for _, ing := range r.Edges.Ingredients {
		qty := ing.Quantity * multiplier
		// Round to 4 decimal places to avoid floating point drift
		qty = math.Round(qty*10000) / 10000

		items = append(items, stock.ConsumptionItem{
			SKU:      ing.ItemSku,
			Quantity: qty,
		})
	}

	c.log.Debug("BOM exploded",
		zap.String("recipe_sku", sku),
		zap.Float64("sale_qty", saleQty),
		zap.Float64("output_qty", r.OutputQty),
		zap.Int("ingredients", len(items)),
	)

	return items, nil
}
