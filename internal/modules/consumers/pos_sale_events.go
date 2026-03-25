package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
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
}

// POSSaleEventsConsumer consumes pos.sale.finalized events to record stock consumption.
type POSSaleEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	orm      *ent.Client
}

// NewPOSSaleEventsConsumer creates a new POS sale events consumer.
func NewPOSSaleEventsConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *POSSaleEventsConsumer {
	return &POSSaleEventsConsumer{
		log:      log.Named("consumers.pos_sale_events"),
		stockSvc: stockSvc,
		orm:      orm,
	}
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

	sub, err := js.Subscribe(
		"pos.sale.finalized",
		c.handleMessage,
		nats.Durable(posSalesDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(posSalesAckWait),
		nats.MaxDeliver(posSalesMaxDeliver),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("pos sale events: subscribe: %w", err)
	}
	c.log.Info("pos sale events consumer started", zap.String("durable", posSalesDurableConsumer))

	<-ctx.Done()
	return sub.Unsubscribe()
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
		// Look up the item to determine its type
		itm, err := c.orm.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(si.SKU), item.IsActive(true)).
			Only(ctx)
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

		if itm.Type == item.TypeRECIPE {
			// BOM explosion: look up recipe by SKU, get ingredients, multiply by quantity
			ingredients, err := c.explodeBOM(ctx, tenantID, si.SKU, si.Quantity)
			if err != nil {
				c.log.Error("pos sale events: BOM explosion failed, falling back to direct consumption",
					zap.Error(err),
					zap.String("sku", si.SKU),
				)
				// Fall back to direct consumption of the recipe item itself
				consumptionItems = append(consumptionItems, stock.ConsumptionItem{
					SKU:      si.SKU,
					Quantity: si.Quantity,
				})
				continue
			}
			consumptionItems = append(consumptionItems, ingredients...)
		} else {
			// Direct consumption for non-RECIPE items
			consumptionItems = append(consumptionItems, stock.ConsumptionItem{
				SKU:      si.SKU,
				Quantity: si.Quantity,
			})
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
