package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	entib "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
)

// ─── Goods Receipt Notes (GRN) + 3-way match (procurement) ──────────────────
// A GRN records the physical receipt of goods against a PO. Posting a GRN moves
// accepted stock in, advances PO line quantity_received + PO status, and (on full
// receipt) emits the enriched purchase_order.received event treasury consumes for
// supplier auto-payout. 3-way match compares ordered ↔ received ↔ invoiced.

type grnLinePayload struct {
	PurchaseOrderLineID *uuid.UUID `json:"purchase_order_line_id"`
	ItemID              uuid.UUID  `json:"item_id"`
	QuantityReceived    float64    `json:"quantity_received"`
	QuantityAccepted    float64    `json:"quantity_accepted"`
	QuantityRejected    float64    `json:"quantity_rejected"`
	UnitCost            float64    `json:"unit_cost"`
	RejectionReason     string     `json:"rejection_reason"`
}

type grnPayload struct {
	WarehouseID *uuid.UUID       `json:"warehouse_id"`
	ReceivedBy  *uuid.UUID       `json:"received_by"`
	Notes       string           `json:"notes"`
	Lines       []grnLinePayload `json:"lines"`
}

type grnLineDTO struct {
	ID                  uuid.UUID  `json:"id"`
	PurchaseOrderLineID *uuid.UUID `json:"purchase_order_line_id"`
	ItemID              uuid.UUID  `json:"item_id"`
	QuantityReceived    float64    `json:"quantity_received"`
	QuantityAccepted    float64    `json:"quantity_accepted"`
	QuantityRejected    float64    `json:"quantity_rejected"`
	UnitCost            float64    `json:"unit_cost"`
	RejectionReason     string     `json:"rejection_reason"`
}

type grnDTO struct {
	ID              uuid.UUID    `json:"id"`
	GrnNumber       string       `json:"grn_number"`
	PurchaseOrderID uuid.UUID    `json:"purchase_order_id"`
	SupplierID      *uuid.UUID   `json:"supplier_id"`
	WarehouseID     *uuid.UUID   `json:"warehouse_id"`
	Status          string       `json:"status"`
	Notes           string       `json:"notes"`
	ReceivedDate    time.Time    `json:"received_date"`
	Lines           []grnLineDTO `json:"lines,omitempty"`
}

func grnToDTO(g *ent.GoodsReceipt, lines []*ent.GoodsReceiptLine) grnDTO {
	dto := grnDTO{
		ID: g.ID, GrnNumber: g.GrnNumber, PurchaseOrderID: g.PurchaseOrderID,
		SupplierID: g.SupplierID, WarehouseID: g.WarehouseID, Status: string(g.Status),
		Notes: g.Notes, ReceivedDate: g.ReceivedDate,
	}
	for _, l := range lines {
		dto.Lines = append(dto.Lines, grnLineDTO{
			ID: l.ID, PurchaseOrderLineID: l.PurchaseOrderLineID, ItemID: l.ItemID,
			QuantityReceived: l.QuantityReceived, QuantityAccepted: l.QuantityAccepted,
			QuantityRejected: l.QuantityRejected, UnitCost: l.UnitCost, RejectionReason: l.RejectionReason,
		})
	}
	return dto
}

func (h *InventoryExtrasHandler) registerGoodsReceiptRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/goods-receipts", h.ListGoodsReceipts)
	r.Get("/inventory/goods-receipts/{grnID}", h.GetGoodsReceipt)
	r.With(perm(add)).Post("/inventory/purchase-orders/{poID}/goods-receipts", h.CreateGoodsReceipt)
	r.With(perm(change)).Post("/inventory/goods-receipts/{grnID}/post", h.PostGoodsReceipt)
	r.Get("/inventory/purchase-orders/{poID}/match", h.MatchPurchaseOrder)
}

// applyStockIn upserts a warehouse-scoped InventoryBalance, incrementing on_hand
// + available by qty (shared receipt logic).
func (h *InventoryExtrasHandler) applyStockIn(ctx context.Context, tx *ent.Tx, tenantID, warehouseID, itemID uuid.UUID, qty float64) error {
	if qty <= 0 {
		return nil
	}
	bal, err := tx.InventoryBalance.Query().
		Where(entib.TenantID(tenantID), entib.ItemID(itemID), entib.WarehouseID(warehouseID)).
		First(ctx)
	if ent.IsNotFound(err) {
		_, e := tx.InventoryBalance.Create().
			SetTenantID(tenantID).SetItemID(itemID).SetWarehouseID(warehouseID).
			SetOnHand(qty).SetAvailable(qty).SetReserved(0).Save(ctx)
		return e
	} else if err != nil {
		return err
	}
	_, e := tx.InventoryBalance.UpdateOneID(bal.ID).
		SetOnHand(bal.OnHand + qty).SetAvailable(bal.Available + qty).Save(ctx)
	return e
}

// ListGoodsReceipts handles GET /inventory/goods-receipts.
//
//	@Summary  List goods receipts
//	@Tags     Procurement
//	@Produce  json
//	@Param    purchase_order_id  query  string  false  "Filter by PO"
//	@Param    status             query  string  false  "Filter by status"
//	@Success  200  {object}  map[string]interface{}
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/goods-receipts [get]
func (h *InventoryExtrasHandler) ListGoodsReceipts(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.GoodsReceipt.Query().Where(entgr.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entgr.StatusEQ(entgr.Status(s)))
	}
	if po := r.URL.Query().Get("purchase_order_id"); po != "" {
		if id, e := uuid.Parse(po); e == nil {
			q = q.Where(entgr.PurchaseOrderID(id))
		}
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entgr.FieldReceivedDate)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list goods receipts")
		return
	}
	out := make([]grnDTO, len(rows))
	for i, g := range rows {
		out[i] = grnToDTO(g, nil)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// GetGoodsReceipt handles GET /inventory/goods-receipts/{grnID}.
//
//	@Summary  Get a goods receipt with its lines
//	@Tags     Procurement
//	@Produce  json
//	@Param    grnID  path  string  true  "GRN ID"
//	@Success  200  {object}  grnDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/goods-receipts/{grnID} [get]
func (h *InventoryExtrasHandler) GetGoodsReceipt(w http.ResponseWriter, r *http.Request) {
	tenantID, g, ok := h.loadGRN(w, r)
	if !ok {
		return
	}
	lines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID), entgrl.TenantID(tenantID)).All(r.Context())
	writeJSON(w, http.StatusOK, grnToDTO(g, lines))
}

// CreateGoodsReceipt handles POST /inventory/purchase-orders/{poID}/goods-receipts.
//
//	@Summary  Create a draft goods receipt against a purchase order
//	@Tags     Procurement
//	@Accept   json
//	@Produce  json
//	@Param    poID  path  string  true  "Purchase order ID"
//	@Param    body  body  grnPayload  true  "GRN payload"
//	@Success  201  {object}  grnDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/purchase-orders/{poID}/goods-receipts [post]
func (h *InventoryExtrasHandler) CreateGoodsReceipt(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.orm.PurchaseOrder.Query().Where(entpo.ID(poID), entpo.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	var req grnPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Lines) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "At least one received line is required")
		return
	}
	grnNumber := "GRN-" + strings.ToUpper(uuid.New().String()[:8])
	warehouseID := po.WarehouseID
	if req.WarehouseID != nil {
		warehouseID = *req.WarehouseID
	}
	create := h.orm.GoodsReceipt.Create().
		SetTenantID(tenantID).SetGrnNumber(grnNumber).SetPurchaseOrderID(poID).
		SetSupplierID(po.SupplierID).SetWarehouseID(warehouseID).SetNotes(req.Notes)
	if req.ReceivedBy != nil {
		create = create.SetReceivedBy(*req.ReceivedBy)
	}
	g, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create GRN failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create goods receipt")
		return
	}
	for _, l := range req.Lines {
		accepted := l.QuantityAccepted
		if accepted == 0 && l.QuantityRejected == 0 {
			accepted = l.QuantityReceived // default: all received are accepted
		}
		lc := h.orm.GoodsReceiptLine.Create().
			SetTenantID(tenantID).SetGoodsReceiptID(g.ID).SetItemID(l.ItemID).
			SetQuantityReceived(l.QuantityReceived).SetQuantityAccepted(accepted).
			SetQuantityRejected(l.QuantityRejected).SetUnitCost(l.UnitCost).SetRejectionReason(l.RejectionReason)
		if l.PurchaseOrderLineID != nil {
			lc = lc.SetPurchaseOrderLineID(*l.PurchaseOrderLineID)
		}
		if _, err := lc.Save(r.Context()); err != nil {
			h.log.Warn("create GRN line failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "goods_receipt", g.ID, "inventory.goods_receipt.created", map[string]any{
		"id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": poID,
	})
	lines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).All(r.Context())
	writeJSON(w, http.StatusCreated, grnToDTO(g, lines))
}

// PostGoodsReceipt handles POST /inventory/goods-receipts/{grnID}/post.
//
//	@Summary  Post a goods receipt — stock-in accepted qty + advance the PO
//	@Tags     Procurement
//	@Produce  json
//	@Param    grnID  path  string  true  "GRN ID"
//	@Success  200  {object}  grnDTO
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/goods-receipts/{grnID}/post [post]
func (h *InventoryExtrasHandler) PostGoodsReceipt(w http.ResponseWriter, r *http.Request) {
	tenantID, g, ok := h.loadGRN(w, r)
	if !ok {
		return
	}
	if g.Status != entgr.StatusDraft {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "Only draft goods receipts can be posted")
		return
	}
	grnLines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).All(r.Context())
	po, err := h.orm.PurchaseOrder.Query().Where(entpo.ID(g.PurchaseOrderID), entpo.TenantID(tenantID)).WithSupplier().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	warehouseID := po.WarehouseID
	if g.WarehouseID != nil {
		warehouseID = *g.WarehouseID
	}

	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to begin transaction")
		return
	}
	for _, l := range grnLines {
		if err = h.applyStockIn(r.Context(), tx, tenantID, warehouseID, l.ItemID, l.QuantityAccepted); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update inventory balance")
			return
		}
		if l.PurchaseOrderLineID != nil {
			if _, err = tx.PurchaseOrderLine.UpdateOneID(*l.PurchaseOrderLineID).AddQuantityReceived(l.QuantityAccepted).Save(r.Context()); err != nil {
				_ = tx.Rollback()
				writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update PO line")
				return
			}
		}
	}
	if _, err = tx.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusPosted).Save(r.Context()); err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to mark GRN posted")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit")
		return
	}

	// Recompute PO status from line receipts (fully vs partially received).
	poLines, _ := h.orm.PurchaseOrderLine.Query().Where(entpoline.PoID(po.ID)).All(r.Context())
	fully := len(poLines) > 0
	anyReceived := false
	for _, pl := range poLines {
		if pl.QuantityReceived > 0 {
			anyReceived = true
		}
		if pl.QuantityReceived < pl.QuantityOrdered {
			fully = false
		}
	}
	newStatus := po.Status.String()
	if fully {
		newStatus = "received"
	} else if anyReceived {
		newStatus = "partially_received"
	}
	if newStatus != po.Status.String() {
		_, _ = h.orm.PurchaseOrder.UpdateOneID(po.ID).SetStatus(entpo.Status(newStatus)).Save(r.Context())
	}

	h.publishOutbox(r.Context(), tenantID, "goods_receipt", g.ID, "inventory.goods_receipt.posted", map[string]any{
		"id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": po.ID, "po_status": newStatus,
	})
	// On full receipt, emit the enriched purchase_order.received event for treasury auto-payout.
	if fully {
		h.emitPurchaseOrderReceived(tenantID, po)
	}

	lines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).All(r.Context())
	writeJSON(w, http.StatusOK, grnToDTO(g, lines))
}

// emitPurchaseOrderReceived writes the enriched purchase_order.received outbox
// event (supplier payment details) that treasury consumes for auto-payout.
func (h *InventoryExtrasHandler) emitPurchaseOrderReceived(tenantID uuid.UUID, po *ent.PurchaseOrder) {
	payload := map[string]any{
		"po_id": po.ID, "po_number": po.PoNumber, "tenant_id": tenantID,
		"supplier_id": po.SupplierID, "total_amount": po.TotalAmount, "currency": po.Currency,
	}
	if po.Edges.Supplier != nil {
		s := po.Edges.Supplier
		payload["supplier_name"] = s.Name
		payload["supplier_contact_email"] = s.ContactEmail
		payload["supplier_contact_phone"] = s.ContactPhone
		payload["supplier_payment_method"] = s.PaymentMethodType
		payload["supplier_mpesa_phone"] = s.MpesaPhone
		payload["supplier_mpesa_business_name"] = s.MpesaBusinessName
		payload["supplier_bank_account_number"] = s.BankAccountNumber
		payload["supplier_bank_name"] = s.BankName
		payload["supplier_tax_pin"] = s.TaxPin
		payload["supplier_paystack_recipient_code"] = s.PaystackRecipientCode
		payload["requires_invoice_before_payment"] = s.RequiresInvoiceBeforePayment
		payload["auto_pay_enabled"] = s.AutoPayEnabled
	}
	go func() {
		evt := &events.Event{
			ID: uuid.New(), TenantID: tenantID, AggregateType: "inventory",
			AggregateID: po.ID, EventType: "purchase_order.received", Payload: payload, Timestamp: time.Now().UTC(),
		}
		raw, e := evt.ToJSON()
		if e != nil {
			return
		}
		_, _ = h.orm.OutboxEvent.Create().
			SetID(evt.ID).SetTenantID(tenantID).SetAggregateType(evt.AggregateType).
			SetAggregateID(evt.AggregateID.String()).SetEventType(evt.EventType).
			SetPayload(json.RawMessage(raw)).SetStatus("PENDING").SetCreatedAt(evt.Timestamp).
			Save(context.Background())
	}()
}

// MatchPurchaseOrder handles GET /inventory/purchase-orders/{poID}/match.
// 3-way match: ordered (PO) ↔ received (PO line quantity_received) ↔ invoiced
// (passed in by the caller, since invoices are owned by treasury).
//
//	@Summary  3-way match for a purchase order
//	@Tags     Procurement
//	@Produce  json
//	@Param    poID          path   string  true   "Purchase order ID"
//	@Param    invoiced_qty  query  number  false  "Total invoiced quantity"
//	@Param    invoice_total query  number  false  "Invoice total amount"
//	@Success  200  {object}  map[string]interface{}
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/purchase-orders/{poID}/match [get]
func (h *InventoryExtrasHandler) MatchPurchaseOrder(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.orm.PurchaseOrder.Query().Where(entpo.ID(poID), entpo.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	poLines, _ := h.orm.PurchaseOrderLine.Query().Where(entpoline.PoID(po.ID)).All(r.Context())
	invoicedQty, _ := strconv.Atoi(r.URL.Query().Get("invoiced_qty"))
	invoiceTotal, _ := strconv.ParseFloat(r.URL.Query().Get("invoice_total"), 64)

	type lineMatch struct {
		ItemID   uuid.UUID `json:"item_id"`
		Ordered  float64   `json:"ordered"`
		Received float64   `json:"received"`
		Status   string    `json:"status"`
	}
	lineMatches := make([]lineMatch, 0, len(poLines))
	totalOrdered, totalReceived := 0.0, 0.0
	for _, pl := range poLines {
		totalOrdered += pl.QuantityOrdered
		totalReceived += pl.QuantityReceived
		st := "matched"
		switch {
		case pl.QuantityReceived > pl.QuantityOrdered:
			st = "over_received"
		case pl.QuantityReceived < pl.QuantityOrdered:
			st = "under_received"
		}
		lineMatches = append(lineMatches, lineMatch{ItemID: pl.ItemID, Ordered: pl.QuantityOrdered, Received: pl.QuantityReceived, Status: st})
	}

	header := "matched"
	switch {
	case totalReceived > totalOrdered:
		header = "over_received"
	case totalReceived < totalOrdered:
		header = "under_received"
	}
	// price/qty variance vs the invoice figures the caller supplied (from treasury).
	if invoiceTotal > 0 && invoiceTotal != po.TotalAmount {
		header = "price_variance"
	}
	if invoicedQty > 0 && float64(invoicedQty) != totalReceived {
		header = "qty_variance"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"purchase_order_id": po.ID,
		"po_number":         po.PoNumber,
		"ordered_total":     totalOrdered,
		"received_total":    totalReceived,
		"po_amount":         po.TotalAmount,
		"invoiced_qty":      invoicedQty,
		"invoice_total":     invoiceTotal,
		"status":            header,
		"lines":             lineMatches,
	})
}

func (h *InventoryExtrasHandler) loadGRN(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.GoodsReceipt, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	grnID, err := uuid.Parse(chi.URLParam(r, "grnID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid GRN ID")
		return uuid.Nil, nil, false
	}
	g, err := h.orm.GoodsReceipt.Query().Where(entgr.ID(grnID), entgr.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Goods receipt not found")
		return uuid.Nil, nil, false
	}
	return tenantID, g, true
}
