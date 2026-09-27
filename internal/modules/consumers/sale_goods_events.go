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
	entadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// Sale goods: stock follows the sale. treasury decides, for every document of one sale (its
// invoices, delivery notes and credit notes), how much of each good it moves out of or back into
// stock under the tenant's stock-out policy (goods leave on the invoice, or on delivery), and
// publishes the whole sale as one snapshot on treasury.sale_goods_issued whenever a document of it
// changes. This consumer makes the stock movements match: every movement of a sale carries the
// reference "sale-<root>:<kind>-<document>", and for each document and item the difference between
// what the snapshot asks for and what its reference already moved is adjusted. A document of the
// sale missing from the snapshot (deleted) is brought back to zero. The sync is idempotent and
// converges whatever order documents change in.
//
// Reasons: goods out = transfer_out, goods back = return, goods bought directly for a job =
// transfer_in. None of them posts to the ledger from here (stock.glPostableReason): treasury posts
// the sale's cost of sales and stock relief itself.

const (
	saleGoodsSubject = "treasury.sale_goods_issued"
	saleGoodsDurable = "inventory-sale-goods-sync"
)

func salePrefix(root uuid.UUID) string { return fmt.Sprintf("sale-%s:", root) }

// saleDocReference is the stock-movement reference of one document of a sale.
func saleDocReference(root uuid.UUID, kind string, docID uuid.UUID) string {
	short := map[string]string{"invoice": "inv", "delivery_note": "dn", "credit_note": "cn", "bought": "bought"}[kind]
	if short == "" {
		short = kind
	}
	return fmt.Sprintf("%s%s-%s", salePrefix(root), short, docID)
}

// SaleGoodsConsumer applies treasury's sale snapshots to stock.
type SaleGoodsConsumer struct {
	log        *zap.Logger
	orm        *ent.Client
	stockSvc   *stock.Service
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewSaleGoodsConsumer creates the consumer.
func NewSaleGoodsConsumer(log *zap.Logger, orm *ent.Client, stockSvc *stock.Service) *SaleGoodsConsumer {
	return &SaleGoodsConsumer{log: log.Named("consumers.sale_goods"), orm: orm, stockSvc: stockSvc}
}

// SetFeatureGate wires the subscription entitlement check (fail-open when nil).
func (c *SaleGoodsConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// Start subscribes via a durable queue consumer shared by the replicas.
func (c *SaleGoodsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("treasury"); err != nil {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name: "treasury", Subjects: []string{"treasury.>"}, Retention: nats.LimitsPolicy,
			MaxAge: 72 * time.Hour, Storage: nats.FileStorage,
		}); err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("sale goods: ensure stream: %w", err)
		}
	}
	eventslib.SubscribeQueueWithRebind(c.log, js, "treasury", saleGoodsSubject, saleGoodsDurable, c.handle,
		nats.Durable(saleGoodsDurable), nats.AckExplicit(), nats.AckWait(30*time.Second),
		nats.MaxDeliver(5), nats.DeliverAll())
	c.log.Info("sale goods consumer started", zap.String("subject", saleGoodsSubject))
	<-ctx.Done()
	return nil
}

// saleSnapshotLine is one good a document moves (quantity > 0 out, < 0 back in).
type saleSnapshotLine struct {
	ItemID      string    `json:"item_id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	Quantity    flexFloat `json:"quantity"`
}

// saleSnapshotDoc is one document of the sale.
type saleSnapshotDoc struct {
	Kind           string             `json:"kind"` // invoice | delivery_note | credit_note
	DocumentID     string             `json:"document_id"`
	DocumentNumber string             `json:"document_number"`
	OutletID       string             `json:"outlet_id"`
	Lines          []saleSnapshotLine `json:"lines"`
}

// saleSnapshot is treasury.sale_goods_issued.
type saleSnapshot struct {
	TenantID     string            `json:"tenant_id"`
	RootID       string            `json:"root_id"`
	RootNumber   string            `json:"root_number"`
	Policy       string            `json:"policy"`
	CustomerName string            `json:"customer_name"`
	Documents    []saleSnapshotDoc `json:"documents"`
}

func (c *SaleGoodsConsumer) handle(msg *nats.Msg) {
	ctx := context.Background()
	var env struct {
		TenantID string       `json:"tenant_id"`
		Payload  saleSnapshot `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		_ = msg.Ack() // malformed, never retry
		return
	}
	tenantID, terr := envelopeTenant(env.TenantID, env.Payload.TenantID)
	root, rerr := uuid.Parse(env.Payload.RootID)
	if terr != nil || rerr != nil {
		_ = msg.Ack()
		return
	}
	if c.hasFeature != nil && !c.hasFeature(ctx, tenantID.String(), "basic_inventory_access") {
		_ = msg.Ack()
		return
	}
	if err := c.sync(ctx, tenantID, root, env.Payload); err != nil {
		c.log.Error("sale goods: stock sync failed", zap.Error(err), zap.String("sale", env.Payload.RootNumber))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// movedByReference is what each document reference of a sale has moved so far, per item
// (positive = out). References other than the documents' (e.g. goods bought for the job) are kept
// apart so they are never undone by a document sync.
func (c *SaleGoodsConsumer) movedByReference(ctx context.Context, tenantID, root uuid.UUID) (map[string]map[uuid.UUID]float64, map[string]uuid.UUID, error) {
	adjs, err := c.orm.StockAdjustment.Query().
		Where(entadj.TenantID(tenantID), entadj.ReferenceHasPrefix(salePrefix(root))).
		Select(entadj.FieldItemID, entadj.FieldWarehouseID, entadj.FieldReference, entadj.FieldQuantityChange).
		All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load sale movements: %w", err)
	}
	moved := map[string]map[uuid.UUID]float64{}
	warehouse := map[string]uuid.UUID{} // where each reference moved stock (reversals go back there)
	for _, a := range adjs {
		if strings.Contains(a.Reference, ":bought-") {
			continue
		}
		if moved[a.Reference] == nil {
			moved[a.Reference] = map[uuid.UUID]float64{}
		}
		moved[a.Reference][a.ItemID] -= a.QuantityChange
		warehouse[a.Reference] = a.WarehouseID
	}
	return moved, warehouse, nil
}

// saleDeltas returns, per item, the stock change (positive = in) that brings what a reference
// moved to its target (positive = out). Pure.
func saleDeltas(target, moved map[uuid.UUID]float64) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	for id := range target {
		if d := round4(moved[id] - target[id]); math.Abs(d) > 1e-9 {
			out[id] = d
		}
	}
	for id, m := range moved {
		if _, ok := target[id]; !ok && math.Abs(m) > 1e-9 {
			out[id] = round4(m)
		}
	}
	return out
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func (c *SaleGoodsConsumer) sync(ctx context.Context, tenantID, root uuid.UUID, snap saleSnapshot) error {
	moved, whByRef, err := c.movedByReference(ctx, tenantID, root)
	if err != nil {
		return err
	}
	skus := map[uuid.UUID]string{}
	seen := map[string]bool{}
	for _, d := range snap.Documents {
		docID, err := uuid.Parse(d.DocumentID)
		if err != nil {
			continue
		}
		ref := saleDocReference(root, d.Kind, docID)
		seen[ref] = true
		target := map[uuid.UUID]float64{}
		for _, l := range d.Lines {
			itm := resolveSaleItem(ctx, c.orm, tenantID, l.ItemID, l.SKU)
			if itm == nil || nonStockItemType(itm.Type) {
				continue // a free-typed good or a service has no stock to move
			}
			target[itm.ID] += float64(l.Quantity)
			skus[itm.ID] = itm.Sku
		}
		wh := whByRef[ref]
		if wh == uuid.Nil {
			wh = resolveSaleWarehouse(ctx, c.orm, tenantID, d.OutletID)
		}
		c.apply(ctx, tenantID, ref, wh, saleDeltas(target, moved[ref]), skus,
			fmt.Sprintf("%s %s (sale %s, customer: %s)", strings.ReplaceAll(d.Kind, "_", " "), d.DocumentNumber, snap.RootNumber, snap.CustomerName))
	}
	// Documents no longer in the sale (deleted): whatever they moved goes back.
	for ref, m := range moved {
		if !seen[ref] {
			c.apply(ctx, tenantID, ref, whByRef[ref], saleDeltas(nil, m), skus, "document removed from sale "+snap.RootNumber)
		}
	}
	return nil
}

// apply posts the stock changes for one reference. A single failing item is logged, never fatal.
func (c *SaleGoodsConsumer) apply(ctx context.Context, tenantID uuid.UUID, ref string, warehouseID uuid.UUID, deltas map[uuid.UUID]float64, skus map[uuid.UUID]string, note string) {
	if len(deltas) == 0 {
		return
	}
	if warehouseID == uuid.Nil {
		c.log.Warn("sale goods: no warehouse resolved, stock not moved", zap.String("reference", ref))
		return
	}
	for itemID, change := range deltas {
		sku := skus[itemID]
		if sku == "" {
			itm, err := c.orm.Item.Get(ctx, itemID)
			if err != nil {
				continue
			}
			sku = itm.Sku
		}
		reason, verb := "transfer_out", "Goods out: "
		if change > 0 {
			reason, verb = "return", "Goods back: "
		}
		if _, err := c.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU: sku, Adjustment: change, Reason: reason, Reference: ref, Notes: verb + note, WarehouseID: warehouseID,
		}); err != nil {
			c.log.Warn("sale goods: stock not adjusted", zap.Error(err), zap.String("sku", sku), zap.String("reference", ref))
		}
	}
}
