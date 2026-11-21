package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
)

// Closing a purchase order short (owner, 20 November 2026): the supplier sent part of an order
// (6 of 10) and will not send the rest, because of a shortage rather than damage. Each goods
// receipt already put what arrived into stock; until now nothing settled the outstanding 4, so
// the order stayed "partially received" forever. Closing it short marks it received for what
// actually came, writes the shortfall into its notes and the audit log, and tells treasury, which
// releases the budget still held for the goods that never came instead of counting it as spent.

// poShortLine is one order line that was not delivered in full.
type poShortLine struct {
	ItemID    uuid.UUID
	Ordered   float64
	Received  float64
	Missing   float64
	UnitPrice float64
}

// poShortfall lists the lines received below the ordered quantity, and whether anything was
// received at all.
func poShortfall(lines []*ent.PurchaseOrderLine) (short []poShortLine, anyReceived bool) {
	for _, l := range lines {
		if l.QuantityReceived > 0 {
			anyReceived = true
		}
		if l.QuantityReceived < l.QuantityOrdered {
			short = append(short, poShortLine{
				ItemID: l.ItemID, Ordered: l.QuantityOrdered, Received: l.QuantityReceived,
				Missing: roundDecimal(l.QuantityOrdered - l.QuantityReceived), UnitPrice: l.UnitPrice,
			})
		}
	}
	return short, anyReceived
}

// shortfallValue is what the undelivered goods would have cost at the ordered prices.
func shortfallValue(short []poShortLine) float64 {
	total := 0.0
	for _, s := range short {
		total += s.Missing * s.UnitPrice
	}
	return roundDecimal(total)
}

// closeShortNote appends the shortfall to the order's notes, one line per item.
func closeShortNote(existing string, short []poShortLine, names map[uuid.UUID]string, reason string, on time.Time) string {
	var b strings.Builder
	if s := strings.TrimSpace(existing); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Closed short on %s", on.Format("2006-01-02"))
	if r := strings.TrimSpace(reason); r != "" {
		fmt.Fprintf(&b, " (%s)", r)
	}
	b.WriteString(":")
	for _, s := range short {
		name := names[s.ItemID]
		if name == "" {
			name = s.ItemID.String()[:8]
		}
		fmt.Fprintf(&b, "\n- %s: %s of %s received, %s not delivered", name,
			formatQty(s.Received), formatQty(s.Ordered), formatQty(s.Missing))
	}
	return b.String()
}

type closeShortRequest struct {
	Reason string `json:"reason"`
}

// CloseShortPurchaseOrder handles POST /inventory/purchase-orders/{poID}/close-short.
//
//	@Summary  Close a partially received purchase order short
//	@Tags     Procurement
//	@Accept   json
//	@Produce  json
//	@Param    poID  path  string             true   "Purchase order ID"
//	@Param    body  body  closeShortRequest  false  "Why the rest will not come"
//	@Success  200  {object}  map[string]interface{}
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/purchase-orders/{poID}/close-short [post]
func (h *InventoryExtrasHandler) CloseShortPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	var req closeShortRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
			return
		}
	}
	if len(req.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Keep the reason under 500 characters")
		return
	}
	// A receipt still open would put more stock in after the order is settled.
	if live, lerr := h.liveOpenGoodsReceipts(ctx, tenantID, poID); lerr == nil && len(live) > 0 {
		writeError(w, http.StatusConflict, "RECEIPT_OPEN",
			"Goods receipt "+live[0].GrnNumber+" is open for this order. Post or cancel it before closing the order short.")
		return
	}

	tx, err := h.orm.Tx(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CLOSE_FAILED", "Failed to close the order")
		return
	}
	defer func() { _ = tx.Rollback() }()
	po, err := tx.PurchaseOrder.Query().Where(entpo.ID(poID), entpo.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status != entpo.StatusPartiallyReceived {
		writeError(w, http.StatusConflict, "INVALID_STATUS",
			"Only a partially received order can be closed short. An order with nothing received is cancelled instead.")
		return
	}
	lines, err := tx.PurchaseOrderLine.Query().Where(entpoline.PoID(poID)).All(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CLOSE_FAILED", "Failed to read the order lines")
		return
	}
	short, anyReceived := poShortfall(lines)
	if !anyReceived || len(short) == 0 {
		writeError(w, http.StatusConflict, "INVALID_STATUS", "This order has nothing outstanding to close")
		return
	}
	ids := make([]uuid.UUID, 0, len(short))
	for _, s := range short {
		ids = append(ids, s.ItemID)
	}
	names := map[uuid.UUID]string{}
	if items, ierr := tx.Item.Query().Where(entitem.IDIn(ids...)).All(ctx); ierr == nil {
		for _, it := range items {
			names[it.ID] = it.Name
		}
	}
	now := time.Now()
	notes := closeShortNote(po.Notes, short, names, req.Reason, now)
	// Conditional on the status read above, so two closes (or a close racing a receipt that
	// completes the order) settle it once.
	n, err := tx.PurchaseOrder.Update().
		Where(entpo.ID(poID), entpo.TenantID(tenantID), entpo.StatusEQ(entpo.StatusPartiallyReceived)).
		SetStatus(entpo.StatusReceived).SetNotes(notes).Save(ctx)
	if err != nil {
		h.log.Error("close PO short failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CLOSE_FAILED", "Failed to close the order")
		return
	}
	if n == 0 {
		writeError(w, http.StatusConflict, "INVALID_STATUS", "This order changed while it was being closed. Refresh and try again.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "CLOSE_FAILED", "Failed to close the order")
		return
	}

	missing := make([]map[string]any, 0, len(short))
	for _, s := range short {
		missing = append(missing, map[string]any{
			"item_id": s.ItemID, "name": names[s.ItemID], "ordered": s.Ordered, "received": s.Received, "missing": s.Missing,
		})
	}
	if h.auditSvc != nil {
		h.auditSvc.Record(ctx, audit.Entry{
			TenantID: tenantID, ActorUserID: actorFromContext(ctx),
			Action: "purchase_order.closed_short", EntityType: "purchase_order", EntityID: poID.String(),
			Reason: strings.TrimSpace(req.Reason),
			Before: map[string]any{"status": "partially_received"},
			After:  map[string]any{"status": "received", "not_delivered": missing, "not_delivered_value": shortfallValue(short)},
		})
	}

	// Rebate on what was received, as a full receipt accrues it.
	totalRebate := 0.0
	for _, l := range lines {
		if l.RebatePercent > 0 {
			totalRebate += roundDecimal(l.QuantityReceived * l.UnitPrice * l.RebatePercent / 100.0)
		}
	}
	if full, ferr := h.orm.PurchaseOrder.Query().Where(entpo.ID(poID)).WithSupplier().Only(ctx); ferr == nil {
		h.emitPurchaseOrderReceived(tenantID, full, roundDecimal(totalRebate), map[string]any{
			"closed_short":        true,
			"close_short_reason":  strings.TrimSpace(req.Reason),
			"not_delivered":       missing,
			"not_delivered_value": shortfallValue(short),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "received", "closed_short": true, "not_delivered": missing})
}
