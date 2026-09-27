package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entidem "github.com/bengobox/inventory-service/internal/ent/idempotencykey"
	entbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// Procure for the job. treasury announces treasury.goods_committed once per sale (an accepted
// quotation, a confirmed sales order, or a direct invoice) under the sale's root document. This
// consumer orders ONLY what the business does not already have: each goods line's quantity less
// the available stock, as one draft purchase order per preferred supplier, priced at the buying
// cost. It then reports back (inventory.procure_to_order.evaluated) how much of the job's goods
// cost comes from stock and how much must be bought, which treasury shows on the invoice.
// treasury.job_goods_purchased (the business bought the goods itself, paid from a bank) cancels
// the still-draft purchase orders of that sale so the goods are never bought twice.

const (
	goodsCommittedDurable    = "inventory-goods-committed-procure-to-order"
	goodsCommittedAckWait    = 30 * time.Second
	goodsCommittedMaxDeliver = 3
	goodsCommittedSubject    = "treasury.goods_committed"

	jobGoodsPurchasedDurable = "inventory-job-goods-purchased"
	jobGoodsPurchasedSubject = "treasury.job_goods_purchased"
)

// goodsCommittedLine is one committed goods line. item_id may be empty (resolve by sku); a line
// that resolves to no item is still goods the business must buy (counted in to_buy, no PO line).
type goodsCommittedLine struct {
	ItemID      string    `json:"item_id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	Quantity    flexFloat `json:"quantity"`
	UnitCost    flexFloat `json:"unit_cost"` // treasury's buying-cost snapshot; item cost when 0
}

// goodsCommittedPayload is the inner payload of treasury.goods_committed.
type goodsCommittedPayload struct {
	TenantID     string `json:"tenant_id"`
	RootID       string `json:"root_id"`
	RootNumber   string `json:"root_number"`
	SourceType   string `json:"source_type"`
	InvoiceID    string `json:"invoice_id"`
	CustomerName string `json:"customer_name"`
	Currency     string `json:"currency"`
	// OutletID is the selling branch; its warehouse receives the purchase (default warehouse else).
	OutletID string               `json:"outlet_id"`
	Lines    []goodsCommittedLine `json:"lines"`
}

// flexFloat decodes a JSON number or a numeric string (e.g. decimal.Decimal -> "5.00").
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("flexFloat: %w", err)
	}
	*f = flexFloat(v)
	return nil
}

// GoodsCommittedConsumer implements procure-for-the-job.
type GoodsCommittedConsumer struct {
	log      *zap.Logger
	orm      *ent.Client
	stockSvc *stock.Service
	// hasFeature gates procurement by subscription entitlement. Fail-open when nil.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewGoodsCommittedConsumer creates the consumer.
func NewGoodsCommittedConsumer(log *zap.Logger, orm *ent.Client, stockSvc *stock.Service) *GoodsCommittedConsumer {
	return &GoodsCommittedConsumer{log: log.Named("consumers.goods_committed"), orm: orm, stockSvc: stockSvc}
}

// SetFeatureGate wires the subscription entitlement check.
func (c *GoodsCommittedConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

func (c *GoodsCommittedConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
}

// Start subscribes to both treasury subjects via durable queue consumers (shared by replicas).
func (c *GoodsCommittedConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("treasury"); err != nil {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name: "treasury", Subjects: []string{"treasury.>"}, Retention: nats.LimitsPolicy,
			MaxAge: 72 * time.Hour, Storage: nats.FileStorage,
		}); err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("goods committed: ensure stream: %w", err)
		}
	}
	eventslib.SubscribeQueueWithRebind(c.log, js, "treasury", goodsCommittedSubject, goodsCommittedDurable, c.handleCommitted,
		nats.Durable(goodsCommittedDurable), nats.AckExplicit(), nats.AckWait(goodsCommittedAckWait),
		nats.MaxDeliver(goodsCommittedMaxDeliver), nats.DeliverAll())
	eventslib.SubscribeQueueWithRebind(c.log, js, "treasury", jobGoodsPurchasedSubject, jobGoodsPurchasedDurable, c.handlePurchased,
		nats.Durable(jobGoodsPurchasedDurable), nats.AckExplicit(), nats.AckWait(goodsCommittedAckWait),
		nats.MaxDeliver(goodsCommittedMaxDeliver), nats.DeliverAll())
	c.log.Info("procure-for-the-job consumers started",
		zap.String("committed", goodsCommittedSubject), zap.String("purchased", jobGoodsPurchasedSubject))
	<-ctx.Done()
	return nil
}

// envelopeTenant returns the tenant from the shared-events envelope, else the payload copy.
func envelopeTenant(top, inner string) (uuid.UUID, error) {
	if top == "" {
		top = inner
	}
	return uuid.Parse(top)
}

func (c *GoodsCommittedConsumer) handleCommitted(msg *nats.Msg) {
	ctx := context.Background()
	var env struct {
		TenantID string                `json:"tenant_id"`
		Payload  goodsCommittedPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn("goods committed: unmarshal failed", zap.Error(err))
		_ = msg.Ack() // malformed, never retry
		return
	}
	tenantID, err := envelopeTenant(env.TenantID, env.Payload.TenantID)
	rootID, rerr := uuid.Parse(env.Payload.RootID)
	if err != nil || rerr != nil {
		c.log.Warn("goods committed: invalid tenant or root id", zap.String("root", env.Payload.RootID))
		_ = msg.Ack()
		return
	}
	if !c.entitled(ctx, tenantID) {
		_ = msg.Ack()
		return
	}
	if err := c.procure(ctx, tenantID, rootID, env.Payload); err != nil {
		c.log.Error("goods committed: procurement failed", zap.Error(err), zap.String("root", env.Payload.RootNumber))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// planLine is a committed line after stock allocation.
type planLine struct {
	item      *ent.Item          // nil when the line resolves to no inventory item
	line      goodsCommittedLine // the committed line as treasury sent it
	quantity  float64
	fromStock float64
	shortfall float64
	unitCost  float64
}

// planProcurement allocates available stock to the committed lines in order (several lines of
// one item share its stock) and returns what is covered from stock and what must be bought. Pure.
func planProcurement(lines []planLine, available map[uuid.UUID]float64) (plan []planLine, fromStockCost, toBuyCost float64) {
	left := make(map[uuid.UUID]float64, len(available))
	for k, v := range available {
		if v > 0 {
			left[k] = v
		}
	}
	for _, l := range lines {
		take := 0.0
		if l.item != nil {
			take = l.quantity
			if left[l.item.ID] < take {
				take = left[l.item.ID]
			}
			left[l.item.ID] -= take
		}
		l.fromStock = take
		l.shortfall = l.quantity - take
		fromStockCost += take * l.unitCost
		toBuyCost += l.shortfall * l.unitCost
		plan = append(plan, l)
	}
	return plan, roundDecimal(fromStockCost), roundDecimal(toBuyCost)
}

// roundDecimal rounds a money amount to 2 decimal places.
func roundDecimal(v float64) float64 {
	return math.Round(v*100) / 100
}

// nonStockItemType reports item types that are never bought through a purchase order.
func nonStockItemType(t entitem.Type) bool {
	return t == entitem.TypeSERVICE || t == entitem.TypeVOUCHER
}

// procure orders the shortfall of one sale, once per root.
func (c *GoodsCommittedConsumer) procure(ctx context.Context, tenantID, rootID uuid.UUID, p goodsCommittedPayload) error {
	// One sale is procured once. The pre-2026-09-27 quotation-accepted key counts as done too, so
	// invoicing an older accepted quotation never orders its goods again.
	legacy, err := c.orm.IdempotencyKey.Query().
		Where(entidem.TenantID(tenantID), entidem.Key("quotation-accepted:"+rootID.String())).Exist(ctx)
	if err != nil {
		return fmt.Errorf("legacy idempotency check: %w", err)
	}
	if legacy {
		return nil
	}
	if _, err := c.orm.IdempotencyKey.Create().
		SetTenantID(tenantID).SetKey("procure-to-order:" + rootID.String()).
		SetEndpoint("consumer:treasury.goods_committed").SetStatus("completed").
		SetExpiresAt(time.Now().Add(365 * 24 * time.Hour)).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil // already procured
		}
		return fmt.Errorf("claim procurement key: %w", err)
	}

	lines := make([]planLine, 0, len(p.Lines))
	itemIDs := make([]uuid.UUID, 0, len(p.Lines))
	unresolved := make([]string, 0)
	for _, gl := range p.Lines {
		qty := float64(gl.Quantity)
		if qty <= 0 {
			continue
		}
		itm := resolveSaleItem(ctx, c.orm, tenantID, gl.ItemID, gl.SKU)
		if itm != nil && nonStockItemType(itm.Type) {
			continue
		}
		cost := float64(gl.UnitCost)
		if cost <= 0 && itm != nil {
			cost = buyingCost(itm)
		}
		if itm == nil {
			unresolved = append(unresolved, gl.Description)
		} else {
			itemIDs = append(itemIDs, itm.ID)
		}
		lines = append(lines, planLine{item: itm, line: gl, quantity: qty, unitCost: cost})
	}

	available := map[uuid.UUID]float64{}
	if len(itemIDs) > 0 {
		bals, err := c.orm.InventoryBalance.Query().
			Where(entbal.TenantID(tenantID), entbal.ItemIDIn(itemIDs...)).All(ctx)
		if err != nil {
			return fmt.Errorf("load stock: %w", err)
		}
		for _, b := range bals {
			available[b.ItemID] += b.Available
		}
	}
	plan, fromStockCost, toBuyCost := planProcurement(lines, available)

	toOrder := make([]resolvedLine, 0, len(plan))
	toBuyLines := make([]map[string]any, 0, len(plan))
	for _, l := range plan {
		if l.shortfall <= 0 {
			continue
		}
		if l.item != nil {
			toOrder = append(toOrder, resolvedLine{item: l.item, quantity: l.shortfall, unitCost: l.unitCost})
		}
		// Per-line quantities still to buy: treasury pre-fills "Buy goods for this job" with them.
		toBuyLines = append(toBuyLines, map[string]any{
			"item_id": l.line.ItemID, "sku": l.line.SKU, "description": l.line.Description,
			"quantity": roundDecimal(l.shortfall), "unit_cost": l.unitCost,
		})
	}
	poIDs, poNumbers, err := c.createPurchaseOrders(ctx, tenantID, rootID, p, toOrder)
	if err != nil {
		return err
	}

	c.writeOutbox(ctx, tenantID, rootID, "procure_to_order.evaluated", map[string]any{
		"tenant_id":        tenantID.String(),
		"root_id":          rootID.String(),
		"root_number":      p.RootNumber,
		"invoice_id":       p.InvoiceID,
		"goods_cost":       roundDecimal(fromStockCost + toBuyCost),
		"from_stock_cost":  fromStockCost,
		"to_buy_cost":      toBuyCost,
		"po_ids":           poIDs,
		"po_numbers":       poNumbers,
		"unresolved_lines": unresolved,
		"to_buy_lines":     toBuyLines,
	})
	c.log.Info("procure for the job evaluated",
		zap.String("root", p.RootNumber), zap.String("source", p.SourceType),
		zap.Float64("from_stock_cost", fromStockCost), zap.Float64("to_buy_cost", toBuyCost),
		zap.Int("purchase_orders", len(poIDs)), zap.Int("unresolved_lines", len(unresolved)))
	return nil
}

// createPurchaseOrders cuts one draft purchase order per preferred supplier for the shortfall.
func (c *GoodsCommittedConsumer) createPurchaseOrders(ctx context.Context, tenantID, rootID uuid.UUID, p goodsCommittedPayload, toOrder []resolvedLine) ([]string, []string, error) {
	groups, order := groupBySupplier(toOrder)
	if len(groups) == 0 {
		return []string{}, []string{}, nil
	}
	warehouseID := resolveSaleWarehouse(ctx, c.orm, tenantID, p.OutletID)
	currency := p.Currency
	if currency == "" {
		currency = "KES"
	}
	stem := p.RootNumber
	if stem == "" {
		stem = strings.ToUpper(rootID.String()[:8])
	}
	ids, numbers := make([]string, 0, len(order)), make([]string, 0, len(order))
	for _, supKey := range order {
		lines := groups[supKey]
		total := 0.0
		for _, l := range lines {
			total += l.unitCost * l.quantity
		}
		suffix, supplierNote := "UNASSIGNED", "No preferred supplier on these items: assign a supplier before issuing."
		var supplierID *uuid.UUID
		if supKey != uuid.Nil {
			sid := supKey
			supplierID = &sid
			suffix, supplierNote = strings.ToUpper(supKey.String()[:8]), "Grouped by the items' preferred supplier."
		}
		poNumber := fmt.Sprintf("PTO-%s-%s", stem, suffix)
		create := c.orm.PurchaseOrder.Create().
			SetTenantID(tenantID).SetPoNumber(poNumber).SetStatus("draft").
			SetTotalAmount(roundDecimal(total)).SetCurrency(currency).
			// quotation_id/number hold the sale's root document (quotation, sales order or invoice).
			SetQuotationID(rootID).SetQuotationNumber(p.RootNumber).
			SetNotes(fmt.Sprintf("Procure for the job: %s (customer: %s). Only the quantity not in stock, at buying cost. %s Review before issuing.",
				p.RootNumber, p.CustomerName, supplierNote))
		if supplierID != nil {
			create = create.SetSupplierID(*supplierID)
		}
		if warehouseID != uuid.Nil {
			create = create.SetWarehouseID(warehouseID)
		}
		po, err := create.Save(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("create purchase order for %s: %w", p.RootNumber, err)
		}
		for _, l := range lines {
			if _, lerr := c.orm.PurchaseOrderLine.Create().
				SetPoID(po.ID).SetItemID(l.itemID).SetQuantityOrdered(l.quantity).
				SetUnitPrice(l.unitCost).SetTotalPrice(roundDecimal(l.unitCost * l.quantity)).Save(ctx); lerr != nil {
				// A duplicate item on the sale hits the (po_id,item_id) unique index: keep the PO.
				c.log.Warn("goods committed: purchase order line not created", zap.Error(lerr), zap.Stringer("po_id", po.ID))
			}
		}
		payload := map[string]any{
			"tenant_id": tenantID.String(), "po_id": po.ID.String(), "po_number": poNumber,
			"root_id": rootID.String(), "root_number": p.RootNumber, "customer_name": p.CustomerName,
			"total_amount": roundDecimal(total), "currency": currency, "line_count": len(lines),
			"notification": map[string]any{"target": "admin"},
		}
		if supplierID != nil {
			payload["supplier_id"] = supplierID.String()
		}
		c.writeOutbox(ctx, tenantID, po.ID, "purchase_order.procure_to_order_created", payload)
		ids, numbers = append(ids, po.ID.String()), append(numbers, poNumber)
	}
	return ids, numbers, nil
}

// jobGoodsPurchasedPayload is treasury.job_goods_purchased: goods bought directly for a sale and
// paid from a bank, with the quantities bought.
type jobGoodsPurchasedPayload struct {
	TenantID   string               `json:"tenant_id"`
	RootID     string               `json:"root_id"`
	RootNumber string               `json:"root_number"`
	PurchaseID string               `json:"purchase_id"` // the treasury expense recording the purchase
	OutletID   string               `json:"outlet_id"`
	Lines      []goodsCommittedLine `json:"lines"`
}

// handlePurchased receives goods bought directly for a sale into stock (so the sale's stock-out
// nets to zero instead of going negative) and withdraws what they replace from the sale's draft
// purchase orders. Issued or received orders are real commitments and are left alone.
func (c *GoodsCommittedConsumer) handlePurchased(msg *nats.Msg) {
	ctx := context.Background()
	var env struct {
		TenantID string                   `json:"tenant_id"`
		Payload  jobGoodsPurchasedPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		_ = msg.Ack()
		return
	}
	p := env.Payload
	tenantID, err := envelopeTenant(env.TenantID, p.TenantID)
	rootID, rerr := uuid.Parse(p.RootID)
	purchaseID, perr := uuid.Parse(p.PurchaseID)
	if err != nil || rerr != nil || perr != nil {
		_ = msg.Ack()
		return
	}
	bought, err := c.receiveDirectPurchase(ctx, tenantID, rootID, purchaseID, p)
	if err != nil {
		c.log.Error("job goods purchased: not received", zap.Error(err), zap.String("sale", p.RootNumber))
		_ = msg.Nak()
		return
	}
	if err := c.withdrawFromDrafts(ctx, tenantID, rootID, bought); err != nil {
		c.log.Error("job goods purchased: draft purchase orders not reduced", zap.Error(err), zap.String("sale", p.RootNumber))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// receiveDirectPurchase puts the bought quantities into stock, once per purchase (reference
// "sale-<root>:bought-<purchase>"). Reason transfer_in: treasury already debited Inventory through
// the purchase, so this movement must not post to the ledger again. Returns item -> quantity.
func (c *GoodsCommittedConsumer) receiveDirectPurchase(ctx context.Context, tenantID, rootID, purchaseID uuid.UUID, p jobGoodsPurchasedPayload) (map[uuid.UUID]float64, error) {
	bought := map[uuid.UUID]float64{}
	skus := map[uuid.UUID]string{}
	for _, l := range p.Lines {
		itm := resolveSaleItem(ctx, c.orm, tenantID, l.ItemID, l.SKU)
		if itm == nil || nonStockItemType(itm.Type) || float64(l.Quantity) <= 0 {
			continue
		}
		bought[itm.ID] += float64(l.Quantity)
		skus[itm.ID] = itm.Sku
	}
	ref := saleDocReference(rootID, "bought", purchaseID)
	if done, err := c.orm.StockAdjustment.Query().Where(entadj.TenantID(tenantID), entadj.Reference(ref)).Exist(ctx); err != nil {
		return nil, err
	} else if done || c.stockSvc == nil || len(bought) == 0 {
		return bought, nil
	}
	warehouseID := resolveSaleWarehouse(ctx, c.orm, tenantID, p.OutletID)
	for itemID, qty := range bought {
		if _, err := c.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU: skus[itemID], Adjustment: qty, Reason: "transfer_in", Reference: ref, WarehouseID: warehouseID,
			Notes: fmt.Sprintf("Bought directly for job %s (paid from a bank in treasury).", p.RootNumber),
		}); err != nil {
			c.log.Warn("job goods purchased: stock not received for item", zap.Error(err), zap.String("sku", skus[itemID]))
		}
	}
	return bought, nil
}

// withdrawFromDrafts reduces the sale's draft purchase orders by what was bought directly, deleting
// emptied lines and cancelling orders left with nothing to buy.
func (c *GoodsCommittedConsumer) withdrawFromDrafts(ctx context.Context, tenantID, rootID uuid.UUID, bought map[uuid.UUID]float64) error {
	if len(bought) == 0 {
		return nil
	}
	drafts, err := c.orm.PurchaseOrder.Query().
		Where(entpo.TenantID(tenantID), entpo.QuotationID(rootID), entpo.StatusEQ(entpo.StatusDraft)).
		WithLines().All(ctx)
	if err != nil {
		return err
	}
	left := map[uuid.UUID]float64{}
	for id, q := range bought {
		left[id] = q
	}
	for _, po := range drafts {
		total, kept := 0.0, 0
		for _, l := range po.Edges.Lines {
			take := math.Min(l.QuantityOrdered, left[l.ItemID])
			if take <= 0 {
				total += l.TotalPrice
				kept++
				continue
			}
			left[l.ItemID] -= take
			rest := round4(l.QuantityOrdered - take)
			if rest <= 0 {
				if err := c.orm.PurchaseOrderLine.DeleteOneID(l.ID).Exec(ctx); err != nil {
					return err
				}
				continue
			}
			if err := c.orm.PurchaseOrderLine.UpdateOneID(l.ID).
				SetQuantityOrdered(rest).SetTotalPrice(roundDecimal(rest * l.UnitPrice)).Exec(ctx); err != nil {
				return err
			}
			total += rest * l.UnitPrice
			kept++
		}
		upd := c.orm.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(roundDecimal(total))
		if kept == 0 {
			upd = upd.SetStatus("cancelled").
				SetNotes("Cancelled: the goods for this job were bought directly and paid from a bank account in treasury.")
		}
		if err := upd.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// writeOutbox records an inventory.<eventType> event with the shared-events envelope (the relay
// publishes it on AggregateType + "." + EventType = "inventory.<eventType>").
func (c *GoodsCommittedConsumer) writeOutbox(ctx context.Context, tenantID, aggregateID uuid.UUID, eventType string, payload map[string]any) {
	evt := eventslib.NewEvent(eventType, "inventory", aggregateID, tenantID, payload)
	raw, err := evt.ToJSON()
	if err != nil {
		c.log.Warn("outbox marshal failed", zap.String("event", eventType), zap.Error(err))
		return
	}
	if _, err := c.orm.OutboxEvent.Create().
		SetID(evt.ID).SetTenantID(tenantID).
		SetAggregateType("inventory").SetAggregateID(aggregateID.String()).
		SetEventType(eventType).SetPayload(json.RawMessage(raw)).SetStatus("PENDING").
		Save(ctx); err != nil {
		c.log.Warn("outbox event not written", zap.String("event", eventType), zap.Error(err))
	}
}

// resolvedLine is an item and the quantity/cost to order.
type resolvedLine struct {
	item     *ent.Item
	quantity float64
	unitCost float64
}

// poLine is one line on a draft PurchaseOrder (item resolved to its id).
type poLine struct {
	itemID   uuid.UUID
	quantity float64
	unitCost float64
}

// groupBySupplier groups lines by each item's preferred_supplier_id (uuid.Nil = none) so one
// draft PO is cut per supplier, in stable first-seen order. Pure.
func groupBySupplier(resolved []resolvedLine) (map[uuid.UUID][]poLine, []uuid.UUID) {
	groups := make(map[uuid.UUID][]poLine)
	order := make([]uuid.UUID, 0, 4)
	seen := make(map[uuid.UUID]bool)
	for _, r := range resolved {
		supKey := uuid.Nil
		if r.item.PreferredSupplierID != nil {
			supKey = *r.item.PreferredSupplierID
		}
		if !seen[supKey] {
			seen[supKey] = true
			order = append(order, supKey)
		}
		groups[supKey] = append(groups[supKey], poLine{itemID: r.item.ID, quantity: r.quantity, unitCost: r.unitCost})
	}
	return groups, order
}

// buyingCost returns the item's per-unit buying (cost) price for the PO line.
// Order: explicit cost_price → derived from purchase_price / purchase_pack_size / yield_pct → 0.
func buyingCost(itm *ent.Item) float64 {
	if itm.CostPrice != nil && *itm.CostPrice > 0 {
		return *itm.CostPrice
	}
	if itm.PurchasePrice != nil && itm.PurchasePackSize != nil && *itm.PurchasePackSize > 0 {
		yield := 1.0
		if itm.YieldPct != nil && *itm.YieldPct > 0 && *itm.YieldPct <= 1 {
			yield = *itm.YieldPct
		}
		return *itm.PurchasePrice / *itm.PurchasePackSize / yield
	}
	return 0
}
