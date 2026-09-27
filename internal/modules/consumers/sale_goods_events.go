package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	entadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// Goods leave stock once per sale. treasury expenses an invoice's goods (COGS) when it is issued,
// so the stock must leave at the same moment, whether or not a delivery note is ever dispatched.
//
// Every stock-out of a sale carries a reference starting "sale-<root>:" (root = the sale's first
// document in treasury): "sale-<root>:inv-<invoice>" for an invoice, "sale-<root>:dn-<note>" for a
// delivery note. treasury.sale_goods_issued ("issue" on every send, "reverse" on void) syncs the
// invoice's stock-outs to its quantities net of what delivery notes of the same sale already took
// (a note dispatched from a sales order before invoicing). The sync is idempotent: a resend changes
// nothing, a re-issue after an edit moves only the difference, a void puts the goods back. Once an
// invoice has issued the goods, later delivery notes of the sale move no stock.

const (
	saleGoodsSubject = "treasury.sale_goods_issued"
	saleGoodsDurable = "inventory-sale-goods-issue"
)

func salePrefix(root uuid.UUID) string { return fmt.Sprintf("sale-%s:", root) }

func saleInvoiceReference(root, invoiceID uuid.UUID) string {
	return fmt.Sprintf("%sinv-%s", salePrefix(root), invoiceID)
}

func saleDeliveryReference(root, noteID uuid.UUID) string {
	return fmt.Sprintf("%sdn-%s", salePrefix(root), noteID)
}

// saleStock is what one sale has taken out of stock so far, per item (positive = out).
type saleStock struct {
	byDelivery map[uuid.UUID]float64 // delivery notes of the sale
	byInvoice  map[uuid.UUID]float64 // the given invoice ("" = every invoice of the sale)
	invoiced   bool                  // some invoice of the sale currently holds goods out
}

// saleTaken sums the sale's stock-outs from its adjustments. invoiceRef narrows byInvoice to one
// invoice; "" counts every invoice of the sale.
func (c *DeliveryNoteDispatchedConsumer) saleTaken(ctx context.Context, tenantID, root uuid.UUID, invoiceRef string) (saleStock, error) {
	out := saleStock{byDelivery: map[uuid.UUID]float64{}, byInvoice: map[uuid.UUID]float64{}}
	adjs, err := c.orm.StockAdjustment.Query().
		Where(entadj.TenantID(tenantID), entadj.ReferenceHasPrefix(salePrefix(root))).
		Select(entadj.FieldItemID, entadj.FieldReference, entadj.FieldQuantityChange).
		All(ctx)
	if err != nil {
		return out, fmt.Errorf("load sale stock-outs: %w", err)
	}
	invoicedNet := 0.0
	for _, a := range adjs {
		taken := -a.QuantityChange
		switch {
		case strings.HasPrefix(a.Reference, salePrefix(root)+"dn-"):
			out.byDelivery[a.ItemID] += taken
		case strings.HasPrefix(a.Reference, salePrefix(root)+"inv-"):
			invoicedNet += taken
			if invoiceRef == "" || a.Reference == invoiceRef {
				out.byInvoice[a.ItemID] += taken
			}
		}
	}
	out.invoiced = invoicedNet > 1e-9
	return out, nil
}

// saleGoodsDelta is the stock change per item (negative = out) that makes an invoice's
// stock-outs equal its quantities net of what the sale's delivery notes already took. Items the
// invoice no longer carries (edited out, or a reversal with no lines) go back to stock. Pure.
func saleGoodsDelta(invoiced, byDelivery, byInvoice map[uuid.UUID]float64) map[uuid.UUID]float64 {
	delta := map[uuid.UUID]float64{}
	seen := map[uuid.UUID]bool{}
	for id := range invoiced {
		seen[id] = true
	}
	for id := range byInvoice {
		seen[id] = true
	}
	for id := range seen {
		target := invoiced[id] - byDelivery[id]
		if target < 0 {
			target = 0
		}
		if d := round4(byInvoice[id] - target); d > 1e-9 || d < -1e-9 {
			delta[id] = d // positive: return to stock; negative: take out
		}
	}
	return delta
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// saleGoodsPayload is treasury.sale_goods_issued.
type saleGoodsPayload struct {
	TenantID       string               `json:"tenant_id"`
	Action         string               `json:"action"` // issue | reverse
	RootID         string               `json:"root_id"`
	DocumentID     string               `json:"document_id"`
	DocumentNumber string               `json:"document_number"`
	CustomerName   string               `json:"customer_name"`
	OutletID       string               `json:"outlet_id"`
	Lines          []goodsCommittedLine `json:"lines"`
}

func (c *DeliveryNoteDispatchedConsumer) handleSaleGoods(msg *nats.Msg) {
	ctx := context.Background()
	var env struct {
		TenantID string           `json:"tenant_id"`
		Payload  saleGoodsPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		_ = msg.Ack() // malformed, never retry
		return
	}
	p := env.Payload
	tenantID, terr := envelopeTenant(env.TenantID, p.TenantID)
	root, rerr := uuid.Parse(p.RootID)
	docID, derr := uuid.Parse(p.DocumentID)
	if terr != nil || rerr != nil || derr != nil {
		_ = msg.Ack()
		return
	}
	if !c.entitled(ctx, tenantID) {
		_ = msg.Ack()
		return
	}
	if err := c.syncSaleGoods(ctx, tenantID, root, docID, p); err != nil {
		c.log.Error("sale goods: stock sync failed", zap.Error(err), zap.String("invoice", p.DocumentNumber))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// syncSaleGoods applies saleGoodsDelta for one invoice.
func (c *DeliveryNoteDispatchedConsumer) syncSaleGoods(ctx context.Context, tenantID, root, invoiceID uuid.UUID, p saleGoodsPayload) error {
	ref := saleInvoiceReference(root, invoiceID)
	invoiced := map[uuid.UUID]float64{}
	skus := map[uuid.UUID]string{}
	if p.Action != "reverse" {
		for _, l := range p.Lines {
			itm := c.resolveItem(ctx, tenantID, l.ItemID, l.SKU)
			if itm == nil || nonStockItemType(itm.Type) || float64(l.Quantity) <= 0 {
				continue
			}
			invoiced[itm.ID] += float64(l.Quantity)
			skus[itm.ID] = itm.Sku
		}
	}
	taken, err := c.saleTaken(ctx, tenantID, root, ref)
	if err != nil {
		return err
	}
	delta := saleGoodsDelta(invoiced, taken.byDelivery, taken.byInvoice)
	if len(delta) == 0 {
		return nil
	}
	warehouseID := c.resolveWarehouse(ctx, tenantID, p.OutletID)
	if warehouseID == uuid.Nil {
		c.log.Warn("sale goods: no warehouse resolved, stock not moved", zap.String("invoice", p.DocumentNumber))
		return nil
	}
	for itemID, change := range delta {
		sku := skus[itemID]
		if sku == "" {
			itm, ierr := c.orm.Item.Get(ctx, itemID)
			if ierr != nil {
				continue
			}
			sku = itm.Sku
		}
		reason, note := "transfer_out", "Goods issue: sold on invoice %s (customer: %s)."
		if change > 0 {
			reason, note = "return", "Goods back to stock: invoice %s reversed or reduced (customer: %s)."
		}
		if _, aerr := c.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU: sku, Adjustment: change, Reason: reason, Reference: ref,
			Notes: fmt.Sprintf(note, p.DocumentNumber, p.CustomerName), WarehouseID: warehouseID,
		}); aerr != nil {
			c.log.Warn("sale goods: stock not adjusted for item", zap.Error(aerr), zap.String("sku", sku))
		}
	}
	c.log.Info("sale goods synced to invoice", zap.String("invoice", p.DocumentNumber), zap.String("action", p.Action), zap.Int("items", len(delta)))
	return nil
}
