package handlers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/documents"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// ─── Purchase Returns / supplier RMA (procurement) ──────────────────────────
// Migrated from ERP procurement.purchases PurchaseReturn/PurchaseReturnedItem.

type purchaseReturnLinePayload struct {
	ItemID   uuid.UUID `json:"item_id"`
	Quantity int       `json:"quantity"`
	SubTotal float64   `json:"sub_total"`
}

type purchaseReturnPayload struct {
	PurchaseOrderID *uuid.UUID                  `json:"purchase_order_id"`
	SupplierID      *uuid.UUID                  `json:"supplier_id"`
	Reason          string                      `json:"reason"`
	Lines           []purchaseReturnLinePayload `json:"lines"`
}

type purchaseReturnDTO struct {
	ID              uuid.UUID  `json:"id"`
	ReturnNumber    string     `json:"return_number"`
	PurchaseOrderID *uuid.UUID `json:"purchase_order_id"`
	SupplierID      *uuid.UUID `json:"supplier_id"`
	Reason          string     `json:"reason"`
	ReturnAmount    float64    `json:"return_amount"`
	PaymentStatus   string     `json:"payment_status"`
	DateReturned    time.Time  `json:"date_returned"`
}

func purchaseReturnToDTO(pr *ent.PurchaseReturn) purchaseReturnDTO {
	return purchaseReturnDTO{
		ID: pr.ID, ReturnNumber: pr.ReturnNumber, PurchaseOrderID: pr.PurchaseOrderID,
		SupplierID: pr.SupplierID, Reason: pr.Reason, ReturnAmount: pr.ReturnAmount,
		PaymentStatus: string(pr.PaymentStatus), DateReturned: pr.DateReturned,
	}
}

func (h *InventoryExtrasHandler) registerPurchaseReturnRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/purchase-returns", h.ListPurchaseReturns)
	r.Get("/inventory/purchase-returns/{returnID}", h.GetPurchaseReturn)
	r.With(perm(add)).Post("/inventory/purchase-returns", h.CreatePurchaseReturn)
	r.With(perm(change)).Post("/inventory/purchase-returns/{returnID}/approve", h.ApprovePurchaseReturn)
}

// ListPurchaseReturns handles GET /inventory/purchase-returns.
//
//	@Summary      List purchase returns
//	@Tags         Procurement
//	@Produce      json
//	@Param        payment_status  query     string  false  "Filter by payment status"
//	@Success      200             {object}  map[string]interface{}  "Paginated list of purchaseReturnDTO"
//	@Failure      400             {object}  map[string]string
//	@Failure      500             {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns [get]
func (h *InventoryExtrasHandler) ListPurchaseReturns(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.PurchaseReturn.Query().Where(entpr.TenantID(tenantID))
	if s := r.URL.Query().Get("payment_status"); s != "" {
		q = q.Where(entpr.PaymentStatusEQ(entpr.PaymentStatus(s)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entpr.FieldDateReturned)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list purchase returns")
		return
	}
	out := make([]purchaseReturnDTO, len(rows))
	for i, pr := range rows {
		out[i] = purchaseReturnToDTO(pr)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// GetPurchaseReturn handles GET /inventory/purchase-returns/{returnID}.
//
//	@Summary      Get a purchase return
//	@Tags         Procurement
//	@Produce      json
//	@Param        returnID  path      string  true  "Purchase return ID"
//	@Success      200       {object}  purchaseReturnDTO
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/{returnID} [get]
func (h *InventoryExtrasHandler) GetPurchaseReturn(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, purchaseReturnToDTO(pr))
}

// CreatePurchaseReturn handles POST /inventory/purchase-returns.
//
//	@Summary      Create a purchase return (supplier RMA)
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      purchaseReturnPayload  true  "Purchase return payload"
//	@Success      201   {object}  purchaseReturnDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns [post]
// nextReturnNumber mints a purchase-return number through the tenant-configurable document
// sequence (numeric by default), falling back to a random PRET- token if the sequence is
// unavailable.
func (h *InventoryExtrasHandler) nextReturnNumber(ctx context.Context, tenantID uuid.UUID) string {
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(ctx, tenantID, documents.DocTypePurchaseReturn); derr == nil && n != "" {
			return n
		}
	}
	return "PRET-" + strings.ToUpper(uuid.New().String()[:8])
}

func (h *InventoryExtrasHandler) CreatePurchaseReturn(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req purchaseReturnPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	num := h.nextReturnNumber(r.Context(), tenantID)
	var total float64
	for _, l := range req.Lines {
		total += l.SubTotal
	}
	create := h.orm.PurchaseReturn.Create().
		SetTenantID(tenantID).SetReturnNumber(num).SetReason(req.Reason).
		SetReturnAmount(total).SetReturnAmountDue(total)
	if req.PurchaseOrderID != nil {
		create = create.SetPurchaseOrderID(*req.PurchaseOrderID)
	}
	if req.SupplierID != nil {
		create = create.SetSupplierID(*req.SupplierID)
	}
	pr, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create purchase return failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase return")
		return
	}
	for _, l := range req.Lines {
		if l.ItemID == uuid.Nil {
			continue
		}
		if _, err := h.orm.PurchaseReturnLine.Create().
			SetTenantID(tenantID).SetPurchaseReturnID(pr.ID).SetItemID(l.ItemID).
			SetQuantity(l.Quantity).SetSubTotal(l.SubTotal).
			Save(r.Context()); err != nil {
			h.log.Warn("create purchase return line failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "purchase_return", pr.ID, "inventory.purchase_return.created", map[string]any{
		"id": pr.ID, "return_number": pr.ReturnNumber, "return_amount": total,
	})
	writeJSON(w, http.StatusCreated, purchaseReturnToDTO(pr))
}

// ApprovePurchaseReturn marks the return paid and emits an event so stock is
// decremented (restock-out) by the stock module consumer.
//
//	@Summary      Approve a purchase return and remove goods from stock
//	@Tags         Procurement
//	@Produce      json
//	@Param        returnID  path      string  true  "Purchase return ID"
//	@Success      200       {object}  purchaseReturnDTO
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Failure      500       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/{returnID}/approve [post]
func (h *InventoryExtrasHandler) ApprovePurchaseReturn(w http.ResponseWriter, r *http.Request) {
	tenantID, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	if !h.gateApproval(w, r, tenantID, "purchase_return", pr.ID, pr.ReturnNumber, pr.ReturnAmount) {
		return
	}
	updated, err := h.orm.PurchaseReturn.UpdateOneID(pr.ID).SetPaymentStatus(entpr.PaymentStatusPaid).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to approve purchase return")
		return
	}
	lines, _ := h.orm.PurchaseReturnLine.Query().Where(entprline.PurchaseReturnID(pr.ID)).All(r.Context())
	items := make([]map[string]any, 0, len(lines))
	consumeItems := make([]stock.ConsumptionItem, 0, len(lines))
	for _, l := range lines {
		sku := h.skuForItem(r.Context(), tenantID, l.ItemID)
		// unit price = line subtotal / qty (the price the goods were received at) so treasury
		// can value the KRA stockIO(12) return-to-supplier movement.
		unitPrice := 0.0
		if l.Quantity != 0 {
			unitPrice = l.SubTotal / float64(l.Quantity)
		}
		items = append(items, map[string]any{
			"item_id": l.ItemID, "sku": sku, "quantity": l.Quantity, "unit_price": unitPrice,
		})
		if sku != "" {
			consumeItems = append(consumeItems, stock.ConsumptionItem{SKU: sku, Quantity: float64(l.Quantity)})
		}
	}
	// Goods returned to the supplier leave our stock (stock-out), in-process.
	if h.stockSvc != nil && len(consumeItems) > 0 {
		if _, err := h.stockSvc.RecordConsumption(r.Context(), tenantID, stock.ConsumptionRequest{
			TenantID:       tenantID,
			OrderID:        updated.ID,
			Items:          consumeItems,
			Reason:         "purchase_return",
			IdempotencyKey: "purchase-return-" + updated.ID.String(),
		}); err != nil {
			h.log.Warn("purchase return: stock-out failed", zap.Error(err))
		}
	}
	// Enriched payload so treasury can raise a vendor CREDIT NOTE that nets the supplier payable
	// (return of received goods reduces what we owe). Needs tenant_id/supplier_id/return_amount.
	retPayload := map[string]any{
		"tenant_id":          tenantID,
		"purchase_return_id": updated.ID.String(),
		"id":                 updated.ID,
		"return_number":      updated.ReturnNumber,
		"return_amount":      updated.ReturnAmount,
		"currency":           "KES",
		"items":              items,
	}
	if updated.SupplierID != nil {
		retPayload["supplier_id"] = updated.SupplierID.String()
	}
	if updated.PurchaseOrderID != nil {
		retPayload["purchase_order_id"] = updated.PurchaseOrderID.String()
	}
	h.publishOutbox(r.Context(), tenantID, "purchase_return", updated.ID, "inventory.purchase_return.approved", retPayload)
	writeJSON(w, http.StatusOK, purchaseReturnToDTO(updated))
}

// autoCreateReturnForRejected creates a PENDING supplier return for a posted goods receipt's
// rejected quantities, prefilled with item, qty and the unit cost the goods were received at
// (GRN line unit_cost, falling back to the PO line unit_price). The return stays pending —
// stock-out, the approval gate and the treasury credit note all still run through the normal
// ApprovePurchaseReturn flow. Idempotent: one auto return per GRN (keyed on goods_receipt_id),
// so a GRN re-post never duplicates it. Best-effort by design — a failure here must never fail
// the goods-receipt post itself.
func (h *InventoryExtrasHandler) autoCreateReturnForRejected(
	ctx context.Context,
	tenantID uuid.UUID,
	g *ent.GoodsReceipt,
	po *ent.PurchaseOrder,
	grnLines []*ent.GoodsReceiptLine,
	unitPriceByItem map[uuid.UUID]float64,
) *ent.PurchaseReturn {
	type rejLine struct {
		itemID   uuid.UUID
		qty      float64
		unitCost float64
		reason   string
	}
	var rejected []rejLine
	for _, l := range grnLines {
		if l.QuantityRejected <= 0 {
			continue
		}
		cost := l.UnitCost
		if cost <= 0 {
			cost = unitPriceByItem[l.ItemID]
		}
		rejected = append(rejected, rejLine{itemID: l.ItemID, qty: l.QuantityRejected, unitCost: cost, reason: l.RejectionReason})
	}
	if len(rejected) == 0 {
		return nil
	}
	// Fast-path check to skip building the return in the common case; the real guard is the
	// unique(tenant_id, goods_receipt_id) DB index below, since this Exist() + later Create() is
	// itself a TOCTOU if two posts for the same GRN ever raced this far.
	if exists, _ := h.orm.PurchaseReturn.Query().
		Where(entpr.TenantID(tenantID), entpr.GoodsReceiptID(g.ID)).
		Exist(ctx); exists {
		return nil
	}

	total := 0.0
	reasons := make([]string, 0, len(rejected)+1)
	reasons = append(reasons, "Auto-created for items rejected on goods receipt "+g.GrnNumber)
	for _, rl := range rejected {
		total += roundDecimal(rl.qty * rl.unitCost)
		if rl.reason != "" {
			reasons = append(reasons, rl.reason)
		}
	}
	total = roundDecimal(total)

	num := h.nextReturnNumber(ctx, tenantID)
	create := h.orm.PurchaseReturn.Create().
		SetTenantID(tenantID).SetReturnNumber(num).
		SetReason(strings.Join(reasons, "; ")).
		SetReturnAmount(total).SetReturnAmountDue(total).
		SetGoodsReceiptID(g.ID).
		SetPurchaseOrderID(po.ID)
	if po.SupplierID != nil {
		create = create.SetSupplierID(*po.SupplierID)
	}
	pr, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			h.log.Info("auto purchase return already exists for this GRN, skipping (idempotent)", zap.String("grn", g.GrnNumber))
			return nil
		}
		h.log.Warn("auto purchase return: create failed", zap.String("grn", g.GrnNumber), zap.Error(err))
		return nil
	}
	for _, rl := range rejected {
		if _, err := h.orm.PurchaseReturnLine.Create().
			SetTenantID(tenantID).SetPurchaseReturnID(pr.ID).SetItemID(rl.itemID).
			SetQuantity(int(math.Round(rl.qty))).SetSubTotal(roundDecimal(rl.qty * rl.unitCost)).
			Save(ctx); err != nil {
			h.log.Warn("auto purchase return: create line failed", zap.Error(err))
		}
	}
	h.publishOutbox(ctx, tenantID, "purchase_return", pr.ID, "inventory.purchase_return.created", map[string]any{
		"id": pr.ID, "return_number": pr.ReturnNumber, "return_amount": total,
		"goods_receipt_id": g.ID.String(), "auto_created": true,
	})
	h.log.Info("auto purchase return created for rejected GRN items",
		zap.String("return_number", pr.ReturnNumber), zap.String("grn", g.GrnNumber), zap.Float64("amount", total))
	return pr
}

func (h *InventoryExtrasHandler) loadPurchaseReturn(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.PurchaseReturn, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid return ID")
		return uuid.Nil, nil, false
	}
	pr, err := h.orm.PurchaseReturn.Query().Where(entpr.ID(id), entpr.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase return not found")
		return uuid.Nil, nil, false
	}
	return tenantID, pr, true
}
