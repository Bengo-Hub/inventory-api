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
	entbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/http/handlers"
)

const (
	stockLowDurableConsumer = "inventory-stock-low-autopo"
	stockLowAckWait         = 30 * time.Second
	stockLowMaxDeliver      = 3
)

// stockLowPayload is the payload of an inventory.stock.low outbox event.
type stockLowPayload struct {
	TenantID     string `json:"tenant_id"`
	ItemID       string `json:"item_id"`
	SKU          string  `json:"sku"`
	Name         string  `json:"name"`
	Available    float64 `json:"available"`
	ReorderLevel int     `json:"reorder_level"`
	WarehouseID  string  `json:"warehouse_id"`
}

// StockEventsConsumer consumes inventory.stock.low events and auto-creates
// draft PurchaseOrders when auto_reorder_enabled is set on the InventoryBalance.
type StockEventsConsumer struct {
	log *zap.Logger
	orm *ent.Client
}

// NewStockEventsConsumer creates a new StockEventsConsumer.
func NewStockEventsConsumer(log *zap.Logger, orm *ent.Client) *StockEventsConsumer {
	return &StockEventsConsumer{
		log: log.Named("consumers.stock_events"),
		orm: orm,
	}
}

// Start subscribes to inventory.stock.low via JetStream durable consumer.
func (c *StockEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	_, err := js.StreamInfo("inventory")
	if err != nil {
		c.log.Info("inventory stream not found, creating for stock-low consumer")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "inventory",
			Subjects:  []string{"inventory.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("stock events: ensure stream: %w", err)
		}
	}

	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"inventory",
		"inventory.stock.low",
		stockLowDurableConsumer,
		c.handleMessage,
		nats.Durable(stockLowDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(stockLowAckWait),
		nats.MaxDeliver(stockLowMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("stock low events consumer started", zap.String("durable", stockLowDurableConsumer))

	<-ctx.Done()
	return nil
}

func (c *StockEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var envelope struct {
		Payload   stockLowPayload `json:"payload"`
		EventType string          `json:"event_type"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("stock events: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	p := envelope.Payload
	tenantID, err := uuid.Parse(p.TenantID)
	if err != nil {
		c.log.Warn("stock events: invalid tenant_id", zap.String("raw", p.TenantID))
		_ = msg.Ack()
		return
	}
	itemID, err := uuid.Parse(p.ItemID)
	if err != nil {
		c.log.Warn("stock events: invalid item_id", zap.String("raw", p.ItemID))
		_ = msg.Ack()
		return
	}

	var warehouseID uuid.UUID
	if p.WarehouseID != "" {
		warehouseID, _ = uuid.Parse(p.WarehouseID)
	}

	if err := c.handleLowStock(ctx, tenantID, itemID, warehouseID, p); err != nil {
		c.log.Error("stock events: handle low stock failed",
			zap.Error(err),
			zap.String("sku", p.SKU),
			zap.String("tenant_id", p.TenantID),
		)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}

// handleLowStock auto-creates a draft PO if auto_reorder_enabled and a preferred supplier
// is configured on the InventoryBalance for this item+warehouse.
func (c *StockEventsConsumer) handleLowStock(ctx context.Context, tenantID, itemID, warehouseID uuid.UUID, p stockLowPayload) error {
	// Look up the InventoryBalance to read auto-reorder settings
	balQ := c.orm.InventoryBalance.Query().
		Where(entbal.TenantID(tenantID), entbal.ItemID(itemID))
	if warehouseID != uuid.Nil {
		balQ = balQ.Where(entbal.WarehouseID(warehouseID))
	}
	bal, err := balQ.First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			c.log.Debug("stock events: no balance record, skipping auto-reorder",
				zap.String("sku", p.SKU))
			return nil
		}
		return fmt.Errorf("query balance sku=%s: %w", p.SKU, err)
	}

	if !bal.AutoReorderEnabled {
		c.log.Debug("stock events: auto_reorder_enabled=false, skipping", zap.String("sku", p.SKU))
		return nil
	}
	if bal.PreferredSupplierID == nil {
		c.log.Warn("stock events: auto_reorder_enabled but no preferred_supplier_id",
			zap.String("sku", p.SKU))
		return nil
	}

	supplierID := *bal.PreferredSupplierID
	targetWarehouseID := bal.WarehouseID

	// Fallback to default warehouse when the balance has no warehouse set
	if targetWarehouseID == uuid.Nil {
		wh, err := c.orm.Warehouse.Query().
			Where(entwh.TenantID(tenantID), entwh.IsDefault(true), entwh.IsActive(true)).
			First(ctx)
		if err != nil {
			return fmt.Errorf("resolve default warehouse: %w", err)
		}
		targetWarehouseID = wh.ID
	}

	// Determine reorder quantity: use configured value, fall back to 2× reorder level,
	// then to the tenant's unit-aware default, then to a global fallback of 10.
	reorderQty := bal.ReorderQuantity
	if reorderQty <= 0 {
		reorderQty = bal.ReorderLevel * 2
	}
	if reorderQty <= 0 {
		reorderQty = c.unitDefaultReorderQty(ctx, tenantID, itemID)
	}

	// Generate PO number using date + short item ID suffix for idempotency
	poNumber := fmt.Sprintf("AUTO-%s-%s", time.Now().Format("20060102"), itemID.String()[:8])

	// Duplicate guard: the po_number+tenant_id combination is unique per schema
	poExists, _ := c.orm.PurchaseOrder.Query().
		Where(entpo.TenantID(tenantID), entpo.PoNumber(poNumber)).
		Exist(ctx)
	if poExists {
		c.log.Debug("stock events: auto-PO already exists, skipping",
			zap.String("po_number", poNumber))
		return nil
	}

	// Create draft PurchaseOrder (unit_price=0; buyer fills before issuing to supplier)
	po, err := c.orm.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(supplierID).
		SetWarehouseID(targetWarehouseID).
		SetPoNumber(poNumber).
		SetStatus("draft").
		SetTotalAmount(0).
		SetCurrency("KES").
		SetNotes(fmt.Sprintf(
			"Auto-generated reorder: %s fell below reorder level (%g available, reorder level %d). Review and set unit prices before issuing.",
			p.SKU, p.Available, p.ReorderLevel,
		)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create auto-PO sku=%s: %w", p.SKU, err)
	}

	_, err = c.orm.PurchaseOrderLine.Create().
		SetPoID(po.ID).
		SetItemID(itemID).
		SetQuantityOrdered(float64(reorderQty)).
		SetUnitPrice(0).
		SetTotalPrice(0).
		Save(ctx)
	if err != nil {
		c.log.Warn("stock events: failed to create auto-PO line",
			zap.Error(err), zap.Stringer("po_id", po.ID))
	}

	// Notify admin via outbox → notifications-service
	eventPayload, _ := json.Marshal(map[string]any{
		"tenant_id":        tenantID.String(),
		"po_id":            po.ID.String(),
		"po_number":        poNumber,
		"sku":              p.SKU,
		"item_name":        p.Name,
		"quantity_ordered": reorderQty,
		"supplier_id":      supplierID.String(),
		"warehouse_id":     targetWarehouseID.String(),
		"available":        p.Available,
		"reorder_level":    p.ReorderLevel,
		"notification":     map[string]any{"target": "admin"},
	})
	_, outboxErr := c.orm.OutboxEvent.Create().
		SetTenantID(tenantID).
		SetAggregateType("purchase_order").
		SetAggregateID(po.ID.String()).
		SetEventType("inventory.purchase_order.auto_created").
		SetPayload(json.RawMessage(eventPayload)).
		SetStatus("PENDING").
		Save(ctx)
	if outboxErr != nil {
		c.log.Warn("stock events: failed to write auto-PO outbox event", zap.Error(outboxErr))
	}

	c.log.Info("auto-PO created for low stock",
		zap.String("sku", p.SKU),
		zap.String("po_number", poNumber),
		zap.Int("quantity_ordered", reorderQty),
		zap.Stringer("po_id", po.ID),
	)
	return nil
}

// unitDefaultReorderQty looks up the tenant's per-unit reorder default for the
// given item. Falls back to DefaultUnitReorderLevels, then to 10.
func (c *StockEventsConsumer) unitDefaultReorderQty(ctx context.Context, tenantID, itemID uuid.UUID) int {
	// Fetch item with its unit edge to get the unit abbreviation
	it, err := c.orm.Item.Query().
		Where(entitem.IDEQ(itemID)).
		WithUnits().
		Only(ctx)
	if err != nil {
		c.log.Debug("unitDefaultReorderQty: could not load item", zap.Error(err))
		return 10
	}

	unitAbbr := ""
	if it.Edges.Units != nil {
		unitAbbr = it.Edges.Units.Abbreviation
	}

	// Look up tenant config for custom unit defaults
	cfg, err := c.orm.TenantInventoryConfig.Query().
		Where(entconfig.TenantID(tenantID)).
		Only(ctx)

	var unitDefaults map[string]int
	if err == nil && cfg.UnitReorderDefaults != nil {
		unitDefaults = cfg.UnitReorderDefaults
	} else {
		unitDefaults = handlers.DefaultUnitReorderLevels()
	}

	if unitAbbr != "" {
		if v, ok := unitDefaults[unitAbbr]; ok && v > 0 {
			return v
		}
	}

	// Fall back to the tenant's global default_reorder_level, then 10
	if err == nil && cfg.DefaultReorderLevel > 0 {
		return cfg.DefaultReorderLevel
	}
	return 10
}
