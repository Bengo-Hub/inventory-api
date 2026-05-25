package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
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
	SKU          string `json:"sku"`
	Name         string `json:"name"`
	Available    int    `json:"available"`
	ReorderLevel int    `json:"reorder_level"`
	WarehouseID  string `json:"warehouse_id"`
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

	sub, err := js.Subscribe(
		"inventory.stock.low",
		c.handleMessage,
		nats.Durable(stockLowDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(stockLowAckWait),
		nats.MaxDeliver(stockLowMaxDeliver),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("stock events: subscribe: %w", err)
	}
	c.log.Info("stock low events consumer started", zap.String("durable", stockLowDurableConsumer))

	<-ctx.Done()
	return sub.Unsubscribe()
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

	// Determine reorder quantity: use configured value or default to 2× the reorder threshold
	reorderQty := bal.ReorderQuantity
	if reorderQty <= 0 {
		reorderQty = bal.ReorderLevel * 2
		if reorderQty <= 0 {
			reorderQty = 10
		}
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
			"Auto-generated reorder: %s fell below reorder level (%d available, reorder level %d). Review and set unit prices before issuing.",
			p.SKU, p.Available, p.ReorderLevel,
		)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create auto-PO sku=%s: %w", p.SKU, err)
	}

	_, err = c.orm.PurchaseOrderLine.Create().
		SetPoID(po.ID).
		SetItemID(itemID).
		SetQuantityOrdered(reorderQty).
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
		SetPayload(eventPayload).
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
