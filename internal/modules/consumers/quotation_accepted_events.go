package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

const (
	quotationAcceptedDurableConsumer = "inventory-quotation-accepted-procure-to-order"
	quotationAcceptedAckWait         = 30 * time.Second
	quotationAcceptedMaxDeliver      = 3

	// quotationAcceptedSubject is what treasury-api actually publishes today
	// (shared-events subject "<aggregate>.<event>" = "treasury.quotation_accepted",
	// carried on the "treasury" JetStream stream). The documented contract names the
	// subject "quotation.accepted"; we bind to the real emitted subject so procure-to-order
	// works end-to-end.
	quotationAcceptedSubject = "treasury.quotation_accepted"
)

// quotationAcceptedLine is one quoted line on the accepted quotation. item_id may be empty
// (resolve by sku then). quantity/unit_price arrive as JSON numbers OR strings (treasury sends
// decimal.Decimal which marshals to a string), so they are decoded flexibly.
type quotationAcceptedLine struct {
	ItemID      string    `json:"item_id"`
	SKU         string    `json:"sku"`
	Description string    `json:"description"`
	Quantity    flexFloat `json:"quantity"`
	UnitPrice   flexFloat `json:"unit_price"` // selling price — informational only; PO uses buying cost
}

// quotationAcceptedPayload is the inner payload of the quotation-accepted event.
type quotationAcceptedPayload struct {
	TenantID        string `json:"tenant_id"`
	QuotationID     string `json:"quotation_id"`
	QuotationNumber string `json:"quotation_number"`
	CustomerName    string `json:"customer_name"`
	CRMCustomerID   string `json:"crm_customer_id"`
	Currency        string `json:"currency"`
	// OutletID is the treasury outlet/branch the quotation belongs to. May be ""/absent for
	// a tenant-wide quotation. Used to pick the receiving warehouse (branch-aware procurement).
	OutletID string                  `json:"outlet_id"`
	Lines    []quotationAcceptedLine `json:"lines"`
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

// QuotationAcceptedConsumer implements procure-to-order: when a treasury sales quotation is
// ACCEPTED, it auto-creates a DRAFT PurchaseOrder to buy the quoted items at their buying
// (cost) price so the business can procure-to-order. Idempotent on quotation_id.
type QuotationAcceptedConsumer struct {
	log *zap.Logger
	orm *ent.Client
	// hasFeature gates procure-to-order by subscription entitlement. Fail-open when nil.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewQuotationAcceptedConsumer creates a new procure-to-order consumer.
func NewQuotationAcceptedConsumer(log *zap.Logger, orm *ent.Client) *QuotationAcceptedConsumer {
	return &QuotationAcceptedConsumer{
		log: log.Named("consumers.quotation_accepted"),
		orm: orm,
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate procure-to-order.
func (c *QuotationAcceptedConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may auto-create procure-to-order POs. Fails open when no
// gate is wired (subscriptions-api down / not configured).
func (c *QuotationAcceptedConsumer) entitled(ctx context.Context, tenantID uuid.UUID) bool {
	if c.hasFeature == nil {
		return true
	}
	return c.hasFeature(ctx, tenantID.String(), "basic_inventory_access")
}

// Start subscribes to the quotation-accepted subject via a JetStream durable queue consumer.
func (c *QuotationAcceptedConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	// Ensure the "treasury" stream exists (owned by treasury-api, but may not exist yet at boot).
	_, err := js.StreamInfo("treasury")
	if err != nil {
		c.log.Info("treasury stream not found, creating it for procure-to-order consumer readiness")
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      "treasury",
			Subjects:  []string{"treasury.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("quotation accepted: ensure stream: %w", err)
		}
	}

	// Durable QUEUE consumer so BOTH inventory-api replicas share the consumer (load-balanced,
	// exactly-once) instead of one replica binding and the other looping forever on
	// "consumer is already bound". The durable name is reused as the deliver-group name.
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		quotationAcceptedSubject,
		quotationAcceptedDurableConsumer,
		c.handleMessage,
		nats.Durable(quotationAcceptedDurableConsumer),
		nats.AckExplicit(),
		nats.AckWait(quotationAcceptedAckWait),
		nats.MaxDeliver(quotationAcceptedMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("quotation accepted (procure-to-order) consumer started",
		zap.String("durable", quotationAcceptedDurableConsumer),
		zap.String("subject", quotationAcceptedSubject))

	<-ctx.Done()
	return nil
}

func (c *QuotationAcceptedConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	// shared-events envelope: tenant_id is carried at the top level; payload holds the rest.
	var envelope struct {
		TenantID  string                   `json:"tenant_id"`
		EventType string                   `json:"event_type"`
		Payload   quotationAcceptedPayload `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("quotation accepted: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	p := envelope.Payload
	// Tenant id lives on the envelope (shared-events moves it out of payload); fall back to
	// the payload copy for resilience against contract drift.
	tenantRaw := envelope.TenantID
	if tenantRaw == "" {
		tenantRaw = p.TenantID
	}
	tenantID, err := uuid.Parse(tenantRaw)
	if err != nil {
		c.log.Warn("quotation accepted: invalid tenant_id", zap.String("raw", tenantRaw))
		_ = msg.Ack() // malformed — don't retry
		return
	}
	quotationID, err := uuid.Parse(p.QuotationID)
	if err != nil {
		c.log.Warn("quotation accepted: invalid quotation_id", zap.String("raw", p.QuotationID))
		_ = msg.Ack()
		return
	}

	// Gate procure-to-order by subscription entitlement (fails open if no gate wired).
	if !c.entitled(ctx, tenantID) {
		c.log.Debug("quotation accepted: tenant lacks basic_inventory_access — skipping procure-to-order",
			zap.String("tenant_id", tenantID.String()), zap.String("quotation_id", quotationID.String()))
		_ = msg.Ack()
		return
	}

	if err := c.handleQuotationAccepted(ctx, tenantID, quotationID, p); err != nil {
		c.log.Error("quotation accepted: procure-to-order failed",
			zap.Error(err),
			zap.String("quotation_id", quotationID.String()),
			zap.String("tenant_id", tenantID.String()),
		)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}

// resolvedLine is a quotation line resolved to an inventory item plus its procurement qty/cost.
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

// groupBySupplier groups resolved quotation lines by each item's preferred_supplier_id so the
// caller can cut one draft PO per supplier. Lines whose item has no preferred supplier are keyed
// under uuid.Nil (the supplier-less bucket). It returns the groups plus the supplier keys in
// stable first-seen order so PO creation is deterministic. Pure (no DB) — unit-testable.
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

// handleQuotationAccepted builds DRAFT PurchaseOrders for the accepted quotation, pricing each
// line at the resolved item BUYING cost. Resolved lines are grouped by each item's
// preferred_supplier_id and one PO is cut per supplier (with supplier_id set); lines whose item
// has no preferred supplier go into a single supplier-less PO. Every PO targets the branch-aware
// receiving warehouse (outlet_id match, else tenant default). Idempotent: if ANY PO already
// exists for (tenant_id, quotation_id) the whole handler is skipped (redelivery-safe).
func (c *QuotationAcceptedConsumer) handleQuotationAccepted(ctx context.Context, tenantID, quotationID uuid.UUID, p quotationAcceptedPayload) error {
	// Idempotency: a quotation legitimately fans out into MULTIPLE POs (one per supplier group,
	// per this handler's own doc comment), so quotation_id can't carry a plain unique DB
	// constraint the way a 1:1 relationship could -- purchaseorder's index on quotation_id is
	// deliberately non-unique. The previous guard (Query().Exist() then, many lines later,
	// Create() in a loop) was a classic check-then-act race with nothing closing the window: two
	// JetStream redeliveries of the same quotation.accepted event (AckWait=30s/MaxDeliver=3, and
	// this handler does real work -- item resolution, supplier grouping, warehouse lookup -- well
	// within that window) could both pass the Exist() check and both create a full set of draft
	// POs, duplicating the buying-cost commitment.
	//
	// Fixed by claiming a single atomic key for "this quotation-accepted event has been processed
	// at all" via the SAME (tenant_id, key) unique-constrained idempotency_keys table the HTTP
	// idempotency middleware uses -- one claim covers however many POs this quotation fans out
	// into, so it doesn't need to know about the per-supplier cardinality at all.
	idemKey := "quotation-accepted:" + quotationID.String()
	if _, err := c.orm.IdempotencyKey.Create().
		SetTenantID(tenantID).
		SetKey(idemKey).
		SetEndpoint("consumer:quotation.accepted").
		SetStatus("completed").
		SetExpiresAt(time.Now().Add(30 * 24 * time.Hour)).
		Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			c.log.Info("quotation accepted: already processed for this quotation — skipping (idempotent)",
				zap.String("quotation_id", quotationID.String()),
				zap.String("quotation_number", p.QuotationNumber))
			return nil
		}
		return fmt.Errorf("claim quotation-accepted idempotency key: %w", err)
	}

	// Resolve each quoted line to an inventory item, then GROUP by the item's
	// preferred_supplier_id so we can cut ONE draft PO per supplier (per-vendor split). Lines
	// whose item has no preferred supplier land in the supplier-less bucket (uuid.Nil key).
	resolved := make([]resolvedLine, 0, len(p.Lines))
	for _, ql := range p.Lines {
		itm := c.resolveItem(ctx, tenantID, ql.ItemID, ql.SKU)
		if itm == nil {
			c.log.Warn("quotation accepted: could not resolve inventory item for line — skipping",
				zap.String("item_id", ql.ItemID),
				zap.String("sku", ql.SKU),
				zap.String("quotation_id", quotationID.String()))
			continue
		}
		qty := float64(ql.Quantity)
		if qty <= 0 {
			qty = 1
		}
		cost := buyingCost(itm)
		if cost <= 0 {
			c.log.Warn("quotation accepted: no buying cost for item — using unit_price=0",
				zap.String("sku", itm.Sku), zap.String("item_id", itm.ID.String()))
		}
		resolved = append(resolved, resolvedLine{item: itm, quantity: qty, unitCost: cost})
	}

	groups, order := groupBySupplier(resolved)

	if len(groups) == 0 {
		c.log.Info("quotation accepted: no resolvable inventory items — no PO created",
			zap.String("quotation_id", quotationID.String()),
			zap.String("quotation_number", p.QuotationNumber))
		return nil
	}

	// Branch-aware: resolve the receiving warehouse from the event's outlet_id (active warehouse
	// whose outlet_id matches, for this tenant); fall back to the tenant's default active
	// warehouse when the event has no outlet or no warehouse matches. Applied to every PO.
	warehouseID := c.resolveWarehouse(ctx, tenantID, p.OutletID)

	currency := p.Currency
	if currency == "" {
		currency = "KES"
	}

	// Stable PO-number stem from the quotation for human traceability; per-supplier suffix keeps
	// each PO unique within (quotation, supplier). The (tenant_id, quotation_id) idempotency
	// guard above remains the authoritative redelivery dedupe.
	stem := strings.ToUpper(quotationID.String()[:8])
	if p.QuotationNumber != "" {
		stem = p.QuotationNumber
	}

	createdPOs := 0
	for _, supKey := range order {
		lines := groups[supKey]
		var total float64
		for _, l := range lines {
			total += l.unitCost * l.quantity
		}

		var supplierID *uuid.UUID
		var poNumber, notes string
		if supKey == uuid.Nil {
			// Supplier-less bucket — current behavior; flag for the buyer to assign a supplier.
			poNumber = fmt.Sprintf("PTO-%s-UNASSIGNED", stem)
			notes = fmt.Sprintf(
				"Procure-to-order: auto-generated from accepted sales quotation %s (customer: %s). "+
					"Lines priced at buying cost. No preferred supplier on these items — assign a supplier and review before issuing.",
				p.QuotationNumber, p.CustomerName,
			)
		} else {
			sid := supKey
			supplierID = &sid
			poNumber = fmt.Sprintf("PTO-%s-%s", stem, strings.ToUpper(supKey.String()[:8]))
			notes = fmt.Sprintf(
				"Procure-to-order: auto-generated from accepted sales quotation %s (customer: %s). "+
					"Grouped by item preferred supplier; lines priced at buying cost. Review before issuing.",
				p.QuotationNumber, p.CustomerName,
			)
		}

		create := c.orm.PurchaseOrder.Create().
			SetTenantID(tenantID).
			SetPoNumber(poNumber).
			SetStatus("draft").
			SetTotalAmount(total).
			SetCurrency(currency).
			SetQuotationID(quotationID).
			SetQuotationNumber(p.QuotationNumber).
			SetNotes(notes)
		if supplierID != nil {
			create = create.SetSupplierID(*supplierID)
		}
		if warehouseID != nil {
			create = create.SetWarehouseID(*warehouseID)
		}
		po, err := create.Save(ctx)
		if err != nil {
			return fmt.Errorf("create procure-to-order PO for quotation %s (supplier %s): %w", quotationID, supKey, err)
		}

		for _, l := range lines {
			if _, lerr := c.orm.PurchaseOrderLine.Create().
				SetPoID(po.ID).
				SetItemID(l.itemID).
				SetQuantityOrdered(l.quantity).
				SetUnitPrice(l.unitCost).
				SetTotalPrice(l.unitCost * l.quantity).
				Save(ctx); lerr != nil {
				// Don't fail the whole PO on a single bad line (e.g. duplicate item on the quotation
				// hits the (po_id,item_id) unique index) — log and continue.
				c.log.Warn("quotation accepted: failed to create PO line",
					zap.Error(lerr), zap.Stringer("po_id", po.ID), zap.Stringer("item_id", l.itemID))
			}
		}

		// Notify admin via outbox → notifications-service (one per PO; mirrors the auto-reorder pattern).
		notifPayload := map[string]any{
			"tenant_id":        tenantID.String(),
			"po_id":            po.ID.String(),
			"po_number":        poNumber,
			"quotation_id":     quotationID.String(),
			"quotation_number": p.QuotationNumber,
			"customer_name":    p.CustomerName,
			"total_amount":     total,
			"currency":         currency,
			"line_count":       len(lines),
			"notification":     map[string]any{"target": "admin"},
		}
		if supplierID != nil {
			notifPayload["supplier_id"] = supplierID.String()
		}
		eventPayload, _ := json.Marshal(notifPayload)
		if _, oerr := c.orm.OutboxEvent.Create().
			SetTenantID(tenantID).
			SetAggregateType("purchase_order").
			SetAggregateID(po.ID.String()).
			SetEventType("inventory.purchase_order.procure_to_order_created").
			SetPayload(json.RawMessage(eventPayload)).
			SetStatus("PENDING").
			Save(ctx); oerr != nil {
			c.log.Warn("quotation accepted: failed to write procure-to-order outbox event", zap.Error(oerr))
		}

		createdPOs++
		c.log.Info("procure-to-order PO created from accepted quotation",
			zap.String("quotation_number", p.QuotationNumber),
			zap.String("po_number", poNumber),
			zap.Float64("total_amount", total),
			zap.Int("lines", len(lines)),
			zap.Stringer("po_id", po.ID),
			zap.String("supplier_id", supKeyString(supplierID)),
		)
	}

	c.log.Info("procure-to-order: per-supplier POs created from accepted quotation",
		zap.String("quotation_id", quotationID.String()),
		zap.String("quotation_number", p.QuotationNumber),
		zap.Int("po_count", createdPOs),
		zap.String("outlet_id", p.OutletID),
	)
	return nil
}

// resolveWarehouse picks the receiving warehouse for procure-to-order POs (branch-aware):
//   - if the event carries a valid outlet_id, the active warehouse whose outlet_id matches for
//     this tenant;
//   - otherwise (no outlet, unparseable, or no match) the tenant's default active warehouse.
//
// Returns nil when nothing matches (the buyer assigns a warehouse before issuing).
func (c *QuotationAcceptedConsumer) resolveWarehouse(ctx context.Context, tenantID uuid.UUID, outletIDRaw string) *uuid.UUID {
	if outletIDRaw != "" {
		if outletID, err := uuid.Parse(outletIDRaw); err == nil {
			if wh, werr := c.orm.Warehouse.Query().
				Where(entwh.TenantID(tenantID), entwh.OutletID(outletID), entwh.IsActive(true)).
				First(ctx); werr == nil {
				return &wh.ID
			} else if !ent.IsNotFound(werr) {
				c.log.Warn("quotation accepted: outlet warehouse lookup failed", zap.Error(werr),
					zap.String("outlet_id", outletIDRaw))
			} else {
				c.log.Info("quotation accepted: no warehouse for outlet — falling back to default",
					zap.String("outlet_id", outletIDRaw))
			}
		} else {
			c.log.Warn("quotation accepted: invalid outlet_id — falling back to default warehouse",
				zap.String("outlet_id", outletIDRaw))
		}
	}
	// Fallback: tenant's default active warehouse (optional — leave unset if none configured).
	if wh, werr := c.orm.Warehouse.Query().
		Where(entwh.TenantID(tenantID), entwh.IsDefault(true), entwh.IsActive(true)).
		First(ctx); werr == nil {
		return &wh.ID
	} else if !ent.IsNotFound(werr) {
		c.log.Warn("quotation accepted: default warehouse lookup failed", zap.Error(werr))
	}
	return nil
}

// supKeyString renders a supplier id pointer for logging ("none" when unassigned).
func supKeyString(id *uuid.UUID) string {
	if id == nil {
		return "none"
	}
	return id.String()
}

// resolveItem resolves an inventory item by id first, then by sku within the tenant.
// Returns nil when neither resolves (the line is then skipped, never blocking).
func (c *QuotationAcceptedConsumer) resolveItem(ctx context.Context, tenantID uuid.UUID, itemIDRaw, sku string) *ent.Item {
	if itemIDRaw != "" {
		if id, err := uuid.Parse(itemIDRaw); err == nil {
			if itm, err := c.orm.Item.Query().
				Where(entitem.ID(id), entitem.TenantID(tenantID)).
				Only(ctx); err == nil {
				return itm
			}
		}
	}
	if sku != "" {
		if itm, err := c.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(sku)).
			Only(ctx); err == nil {
			return itm
		}
	}
	return nil
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
