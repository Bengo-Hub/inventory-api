package handlers

import (
	"context"
	"encoding/json"
	"fmt"
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
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	entstockadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// ─── Purchase Returns / supplier RMA (procurement) ──────────────────────────
// Migrated from ERP procurement.purchases PurchaseReturn/PurchaseReturnedItem.

type purchaseReturnLinePayload struct {
	ItemID uuid.UUID `json:"item_id"`
	// LotID optionally targets a specific batch of a lot-tracked item (pharmacy RTV of an
	// expiring batch).
	LotID    *uuid.UUID `json:"lot_id"`
	Quantity float64    `json:"quantity"`
	SubTotal float64    `json:"sub_total"`
}

type purchaseReturnPayload struct {
	PurchaseOrderID *uuid.UUID `json:"purchase_order_id"`
	SupplierID      *uuid.UUID `json:"supplier_id"`
	// WarehouseID is the location the goods leave from. Omitted, it defaults to the operating
	// outlet's own warehouse (X-Outlet-ID), then the tenant default: the same resolution a
	// goods receipt uses, so a return always comes out of the outlet that raised it.
	WarehouseID *uuid.UUID `json:"warehouse_id"`
	Reason      string     `json:"reason"`
	// DateReturned accepts "YYYY-MM-DD" (stored as that calendar day) or RFC3339; when omitted
	// the return defaults to now.
	DateReturned *string                     `json:"date_returned"`
	Lines        []purchaseReturnLinePayload `json:"lines"`
}

// parseFlexibleDate accepts a plain "YYYY-MM-DD" (as sent by an <input type="date">) or a full
// RFC3339 timestamp, the same two formats extras_purchase_orders.go's ExpectedDate accepts.
func parseFlexibleDate(s string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// maxReturnDateForward is the slack allowed past "now" for a return date: a calendar day typed
// in a timezone ahead of UTC (Kenya, UTC+3) is legitimately "tomorrow" in UTC for a few hours.
const maxReturnDateForward = 36 * time.Hour

func (h *InventoryExtrasHandler) registerPurchaseReturnRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/purchase-returns", h.ListPurchaseReturns)
	r.Get("/inventory/purchase-returns/{returnID}", h.GetPurchaseReturn)
	r.Get("/inventory/purchase-returns/{returnID}/pdf", h.GeneratePurchaseReturnPDF)
	r.With(perm(add)).Post("/inventory/purchase-returns", h.CreatePurchaseReturn)
	r.With(perm(change)).Post("/inventory/purchase-returns/{returnID}/approve", h.ApprovePurchaseReturn)
}

// ListPurchaseReturns handles GET /inventory/purchase-returns.
//
//	@Summary      List purchase returns
//	@Tags         Procurement
//	@Produce      json
//	@Param        payment_status  query     string  false  "Filter by payment status"
//	@Param        supplier_id     query     string  false  "Filter by supplier"
//	@Param        warehouse_id    query     string  false  "Filter by the warehouse the goods left from"
//	@Param        outlet_id       query     string  false  "Filter by outlet (any of its warehouses)"
//	@Param        search          query     string  false  "Match the return number"
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
	qp := r.URL.Query()
	q := h.orm.PurchaseReturn.Query().Where(entpr.TenantID(tenantID))
	if s := qp.Get("payment_status"); s != "" {
		q = q.Where(entpr.PaymentStatusEQ(entpr.PaymentStatus(s)))
	}
	if id, e := uuid.Parse(qp.Get("supplier_id")); e == nil {
		q = q.Where(entpr.SupplierID(id))
	}
	var whIDs []uuid.UUID
	if id, e := uuid.Parse(qp.Get("warehouse_id")); e == nil {
		whIDs = []uuid.UUID{id}
	} else if oid, oe := uuid.Parse(qp.Get("outlet_id")); oe == nil {
		whIDs, _ = h.orm.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletIDEQ(oid)).IDs(r.Context())
		if whIDs == nil {
			whIDs = []uuid.UUID{}
		}
	}
	if whIDs != nil {
		// A return raised before warehouse capture carries only its goods receipt's warehouse
		// (the same fallback the DTO and approval use), so match those too.
		grnIDs, _ := h.orm.GoodsReceipt.Query().
			Where(entgr.TenantID(tenantID), entgr.WarehouseIDIn(whIDs...)).IDs(r.Context())
		q = q.Where(entpr.Or(
			entpr.WarehouseIDIn(whIDs...),
			entpr.And(entpr.WarehouseIDIsNil(), entpr.GoodsReceiptIDIn(grnIDs...)),
		))
	}
	if s := strings.TrimSpace(qp.Get("search")); s != "" {
		q = q.Where(entpr.ReturnNumberContainsFold(s))
	}
	if from, to, ok := parseCreatedAtRange(r); ok {
		q = q.Where(entpr.DateReturnedGTE(from), entpr.DateReturnedLTE(to))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entpr.FieldDateReturned), ent.Desc(entpr.FieldReturnNumber)).
		Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list purchase returns")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(h.purchaseReturnsToDTOs(r.Context(), tenantID, rows), total, p))
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
	tenantID, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.purchaseReturnsToDTOs(r.Context(), tenantID, []*ent.PurchaseReturn{pr})[0])
}

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
	ctx := r.Context()

	// Lines: every returned item must be a real item of this tenant with a positive quantity.
	// Previously a bad line was silently dropped after the header was saved, leaving a return
	// whose amount counted items it did not actually list.
	lines := make([]purchaseReturnLinePayload, 0, len(req.Lines))
	itemIDs := make([]uuid.UUID, 0, len(req.Lines))
	for _, l := range req.Lines {
		if l.ItemID == uuid.Nil {
			continue
		}
		if l.Quantity <= 0 || math.IsNaN(l.Quantity) || math.IsInf(l.Quantity, 0) {
			writeError(w, http.StatusBadRequest, "INVALID_QUANTITY", "Every returned item needs a quantity greater than zero")
			return
		}
		if l.SubTotal < 0 || math.IsNaN(l.SubTotal) || math.IsInf(l.SubTotal, 0) {
			writeError(w, http.StatusBadRequest, "INVALID_AMOUNT", "A returned item's sub-total cannot be negative")
			return
		}
		l.Quantity = roundDecimal(l.Quantity)
		l.SubTotal = roundDecimal(l.SubTotal)
		lines = append(lines, l)
		itemIDs = append(itemIDs, l.ItemID)
	}
	if len(lines) == 0 {
		writeError(w, http.StatusBadRequest, "NO_ITEMS", "Add at least one item to return")
		return
	}
	if n, _ := h.orm.Item.Query().Where(entitem.TenantID(tenantID), entitem.IDIn(uniqueUUIDs(itemIDs)...)).Count(ctx); n != len(uniqueUUIDs(itemIDs)) {
		writeError(w, http.StatusBadRequest, "UNKNOWN_ITEM", "One or more returned items were not found")
		return
	}

	warehouseID := uuid.Nil
	if req.WarehouseID != nil && *req.WarehouseID != uuid.Nil {
		if ok, _ := h.orm.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(*req.WarehouseID)).Exist(ctx); !ok {
			writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE", "The selected location was not found")
			return
		}
		warehouseID = *req.WarehouseID
	} else {
		warehouseID = h.resolveReceiptWarehouse(ctx, tenantID)
	}

	var dateReturned *time.Time
	if req.DateReturned != nil && strings.TrimSpace(*req.DateReturned) != "" {
		t, ok := parseFlexibleDate(strings.TrimSpace(*req.DateReturned))
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "date_returned must be YYYY-MM-DD")
			return
		}
		if t.After(time.Now().Add(maxReturnDateForward)) {
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "The return date cannot be in the future")
			return
		}
		dateReturned = &t
	}

	var total float64
	for _, l := range lines {
		total += l.SubTotal
	}
	total = roundDecimal(total)
	num := h.nextReturnNumber(ctx, tenantID)

	// Header + lines in one transaction: a return is only ever saved together with every item
	// it lists.
	tx, err := h.orm.Tx(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase return")
		return
	}
	create := tx.PurchaseReturn.Create().
		SetTenantID(tenantID).SetReturnNumber(num).SetReason(strings.TrimSpace(req.Reason)).
		SetReturnAmount(total).SetReturnAmountDue(total)
	if req.PurchaseOrderID != nil {
		create = create.SetPurchaseOrderID(*req.PurchaseOrderID)
	}
	if req.SupplierID != nil && *req.SupplierID != uuid.Nil {
		create = create.SetSupplierID(*req.SupplierID)
	}
	if warehouseID != uuid.Nil {
		create = create.SetWarehouseID(warehouseID)
	}
	if dateReturned != nil {
		create = create.SetDateReturned(*dateReturned)
	}
	if actor := actorFromRequest(r); actor != uuid.Nil {
		create = create.SetAddedBy(actor)
	}
	pr, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		h.log.Error("create purchase return failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase return")
		return
	}
	for _, l := range lines {
		lc := tx.PurchaseReturnLine.Create().
			SetTenantID(tenantID).SetPurchaseReturnID(pr.ID).SetItemID(l.ItemID).
			SetQuantity(l.Quantity).SetSubTotal(l.SubTotal)
		if l.LotID != nil && *l.LotID != uuid.Nil {
			lc = lc.SetLotID(*l.LotID)
		}
		if _, err := lc.Save(ctx); err != nil {
			_ = tx.Rollback()
			h.log.Error("create purchase return line failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to save the returned items")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase return")
		return
	}
	h.publishOutbox(ctx, tenantID, "purchase_return", pr.ID, "inventory.purchase_return.created", map[string]any{
		"id": pr.ID, "return_number": pr.ReturnNumber, "return_amount": total,
	})
	writeJSON(w, http.StatusCreated, h.purchaseReturnsToDTOs(ctx, tenantID, []*ent.PurchaseReturn{pr})[0])
}

// purchaseReturnWarehouse resolves where a return's goods leave from: the return's own
// warehouse, else the linked goods receipt's warehouse (auto-created rejection returns and
// returns raised before warehouse capture), else the operating outlet's / tenant default.
func (h *InventoryExtrasHandler) purchaseReturnWarehouse(ctx context.Context, tenantID uuid.UUID, pr *ent.PurchaseReturn) uuid.UUID {
	if pr.WarehouseID != nil && *pr.WarehouseID != uuid.Nil {
		return *pr.WarehouseID
	}
	if pr.GoodsReceiptID != nil {
		if grn, err := h.orm.GoodsReceipt.Get(ctx, *pr.GoodsReceiptID); err == nil && grn.WarehouseID != nil {
			return *grn.WarehouseID
		}
	}
	return h.resolveReceiptWarehouse(ctx, tenantID)
}

// ApprovePurchaseReturn approves a return: the goods leave stock as a "purchase_return" stock
// adjustment (so the stock-history ledger shows "Purchase Return" with the return number, not
// a sale) and an event lets treasury raise the supplier credit note and the eTIMS
// return-to-supplier movement.
//
//	@Summary      Approve a purchase return and remove goods from stock
//	@Tags         Procurement
//	@Produce      json
//	@Param        returnID  path      string  true  "Purchase return ID"
//	@Success      200       {object}  purchaseReturnDTO
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Failure      409       {object}  map[string]string
//	@Failure      500       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/{returnID}/approve [post]
func (h *InventoryExtrasHandler) ApprovePurchaseReturn(w http.ResponseWriter, r *http.Request) {
	tenantID, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	if pr.PaymentStatus == entpr.PaymentStatusPaid {
		writeError(w, http.StatusConflict, "ALREADY_APPROVED", "This return has already been approved")
		return
	}
	ctx := r.Context()
	lines, err := h.orm.PurchaseReturnLine.Query().
		Where(entprline.PurchaseReturnID(pr.ID), entprline.TenantID(tenantID)).All(ctx)
	if err != nil || len(lines) == 0 {
		writeError(w, http.StatusBadRequest, "NO_ITEMS", "This return has no items to send back")
		return
	}
	// Resolve every SKU before anything moves, so a return can never end up approved with
	// only some of its items taken out of stock.
	itemIDs := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	type itemInfo struct{ sku, name string }
	itemsByID := map[uuid.UUID]itemInfo{}
	if its, ierr := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(uniqueUUIDs(itemIDs)...)).
		Select(entitem.FieldID, entitem.FieldSku, entitem.FieldName).All(ctx); ierr == nil {
		for _, it := range its {
			itemsByID[it.ID] = itemInfo{sku: it.Sku, name: it.Name}
		}
	}
	for _, l := range lines {
		if itemsByID[l.ItemID].sku == "" {
			writeError(w, http.StatusBadRequest, "UNKNOWN_ITEM", "A returned item no longer exists; edit the return before approving")
			return
		}
	}
	if !h.gateApproval(w, r, tenantID, "purchase_return", pr.ID, pr.ReturnNumber, pr.ReturnAmount) {
		return
	}

	warehouseID := h.purchaseReturnWarehouse(ctx, tenantID, pr)
	if warehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "NO_WAREHOUSE", "No location found to take the returned goods out of")
		return
	}
	// Claim the approval atomically: only the request that flips pending -> paid moves stock, so
	// a double click or two approvers at once can never take the goods out twice.
	claim := h.orm.PurchaseReturn.Update().
		Where(entpr.ID(pr.ID), entpr.TenantID(tenantID), entpr.PaymentStatusNEQ(entpr.PaymentStatusPaid)).
		SetPaymentStatus(entpr.PaymentStatusPaid)
	if pr.WarehouseID == nil {
		claim = claim.SetWarehouseID(warehouseID)
	}
	if n, cerr := claim.Save(ctx); cerr != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to approve purchase return")
		return
	} else if n == 0 {
		writeError(w, http.StatusConflict, "ALREADY_APPROVED", "This return has already been approved")
		return
	}
	updated, err := h.orm.PurchaseReturn.Get(ctx, pr.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to approve purchase return")
		return
	}

	supplierName := ""
	if updated.SupplierID != nil {
		if sup, serr := h.orm.Supplier.Get(ctx, *updated.SupplierID); serr == nil {
			supplierName = sup.Name
		}
	}
	notes := "Returned to supplier"
	if supplierName != "" {
		notes += " " + supplierName
	}
	if updated.Reason != "" {
		notes += ": " + updated.Reason
	}
	actor := actorFromRequest(r)
	items := make([]map[string]any, 0, len(lines))
	stockWarnings := make([]string, 0)
	for _, l := range lines {
		info := itemsByID[l.ItemID]
		// unit price = line subtotal / qty (the price the goods were received at) so treasury
		// can value the KRA stockIO(12) return-to-supplier movement and itemise the credit note.
		unitPrice := 0.0
		if l.Quantity != 0 {
			unitPrice = roundDecimal(l.SubTotal / l.Quantity)
		}
		items = append(items, map[string]any{
			"item_id": l.ItemID, "sku": info.sku, "item_name": info.name,
			"quantity": l.Quantity, "unit_price": unitPrice, "sub_total": l.SubTotal,
		})
		if h.stockSvc == nil {
			continue
		}
		if _, serr := h.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU:         info.sku,
			Adjustment:  -l.Quantity,
			Reason:      string(entstockadj.ReasonPurchaseReturn),
			Reference:   updated.ReturnNumber,
			Notes:       notes,
			AdjustedBy:  actor,
			WarehouseID: warehouseID,
		}); serr != nil {
			h.log.Error("purchase return: stock-out failed",
				zap.String("return_number", updated.ReturnNumber), zap.String("sku", info.sku), zap.Error(serr))
			stockWarnings = append(stockWarnings, fmt.Sprintf("%s: %v", info.name, serr))
		}
	}

	outletID := ""
	if wh, werr := h.orm.Warehouse.Get(ctx, warehouseID); werr == nil && wh.OutletID != nil {
		outletID = wh.OutletID.String()
	}
	// Enriched payload so treasury can raise an itemised vendor CREDIT NOTE dated on the return
	// day that nets the supplier payable, on the right outlet and eTIMS branch.
	retPayload := map[string]any{
		"tenant_id":          tenantID,
		"purchase_return_id": updated.ID.String(),
		"id":                 updated.ID,
		"return_number":      updated.ReturnNumber,
		"return_amount":      updated.ReturnAmount,
		"currency":           "KES",
		"reason":             updated.Reason,
		"date_returned":      updated.DateReturned.UTC().Format("2006-01-02"),
		"supplier_name":      supplierName,
		"warehouse_id":       warehouseID.String(),
		"outlet_id":          outletID, // lets treasury transmit on the right eTIMS branch
		"items":              items,
	}
	if updated.SupplierID != nil {
		retPayload["supplier_id"] = updated.SupplierID.String()
	}
	if updated.PurchaseOrderID != nil {
		retPayload["purchase_order_id"] = updated.PurchaseOrderID.String()
	}
	if actor != uuid.Nil {
		retPayload["approved_by"] = actor.String()
	}
	h.publishOutbox(ctx, tenantID, "purchase_return", updated.ID, "inventory.purchase_return.approved", retPayload)

	dto := h.purchaseReturnsToDTOs(ctx, tenantID, []*ent.PurchaseReturn{updated})[0]
	dto.StockWarnings = stockWarnings
	writeJSON(w, http.StatusOK, dto)
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
			SetQuantity(roundDecimal(rl.qty)).SetSubTotal(roundDecimal(rl.qty * rl.unitCost)).
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

// uniqueUUIDs returns ids with duplicates removed, order preserved.
func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
