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
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	"github.com/bengobox/inventory-service/internal/modules/documents"
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
	// Serials are captured for serial-tracked items: one serial per accepted unit. Each becomes
	// an InventorySerial row (status=available) when the GRN is posted.
	Serials []string `json:"serials,omitempty"`
	// Lot/batch capture for lot-tracked items: a lot number + optional expiry. Becomes an
	// InventoryLot layer (for FIFO/FEFO costing) when the GRN is posted.
	LotNumber  string     `json:"lot_number,omitempty"`
	ExpiryDate *time.Time `json:"expiry_date,omitempty"`
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
	Serials             []string   `json:"serials,omitempty"`
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
			Serials: l.Serials,
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

// dedupeNonEmpty trims, drops empties, and removes duplicate serial strings (case-sensitive),
// preserving order. Keeps serial capture clean before persistence.
func dedupeNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
	var before float64
	if ent.IsNotFound(err) {
		before = 0
		if _, e := tx.InventoryBalance.Create().
			SetTenantID(tenantID).SetItemID(itemID).SetWarehouseID(warehouseID).
			SetOnHand(qty).SetAvailable(qty).SetReserved(0).Save(ctx); e != nil {
			return e
		}
	} else if err != nil {
		return err
	} else {
		before = bal.OnHand
		if _, e := tx.InventoryBalance.UpdateOneID(bal.ID).
			SetOnHand(bal.OnHand + qty).SetAvailable(bal.Available + qty).Save(ctx); e != nil {
			return e
		}
	}
	// Fire stock.updated + low-stock recheck + recipe re-enable cascade. Goods-receipt stock-in
	// previously wrote the balance directly and skipped this, so received stock never re-enabled
	// sold-out recipes or cleared low-stock alerts.
	if h.stockSvc != nil {
		h.stockSvc.EmitStockInCascade(ctx, tx, tenantID, itemID, warehouseID, before, before+qty)
	}
	return nil
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
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypeGRN); derr == nil && n != "" {
			grnNumber = n
		}
	}
	warehouseID := po.WarehouseID
	if req.WarehouseID != nil {
		warehouseID = req.WarehouseID
	}
	create := h.orm.GoodsReceipt.Create().
		SetTenantID(tenantID).SetGrnNumber(grnNumber).SetPurchaseOrderID(poID).
		SetNillableSupplierID(po.SupplierID).SetNillableWarehouseID(warehouseID).SetNotes(req.Notes)
	if req.ReceivedBy != nil {
		create = create.SetReceivedBy(*req.ReceivedBy)
	}
	g, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create GRN failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create goods receipt")
		return
	}
	// Which of the received items are serial-tracked (require one serial per accepted unit).
	lineItemIDs := make([]uuid.UUID, 0, len(req.Lines))
	for _, l := range req.Lines {
		lineItemIDs = append(lineItemIDs, l.ItemID)
	}
	serialTracked := map[uuid.UUID]bool{}
	if items, e := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(lineItemIDs...), entitem.TrackSerialNumbers(true)).
		Select(entitem.FieldID).All(r.Context()); e == nil {
		for _, it := range items {
			serialTracked[it.ID] = true
		}
	}
	for _, l := range req.Lines {
		accepted := l.QuantityAccepted
		if accepted == 0 && l.QuantityRejected == 0 {
			accepted = l.QuantityReceived // default: all received are accepted
		}
		// Serial-tracked items must carry exactly one serial per accepted unit.
		serials := dedupeNonEmpty(l.Serials)
		if serialTracked[l.ItemID] && float64(len(serials)) != accepted {
			_, _ = h.orm.GoodsReceipt.Delete().Where(entgr.ID(g.ID)).Exec(r.Context())
			writeError(w, http.StatusUnprocessableEntity, "SERIAL_COUNT_MISMATCH",
				"serial-tracked item requires one unique serial per accepted unit")
			return
		}
		lc := h.orm.GoodsReceiptLine.Create().
			SetTenantID(tenantID).SetGoodsReceiptID(g.ID).SetItemID(l.ItemID).
			SetQuantityReceived(l.QuantityReceived).SetQuantityAccepted(accepted).
			SetQuantityRejected(l.QuantityRejected).SetUnitCost(l.UnitCost).SetRejectionReason(l.RejectionReason).
			SetSerials(serials).SetLotNumber(strings.TrimSpace(l.LotNumber)).SetNillableExpiryDate(l.ExpiryDate)
		if l.PurchaseOrderLineID != nil {
			lc = lc.SetPurchaseOrderLineID(*l.PurchaseOrderLineID)
		}
		if _, err := lc.Save(r.Context()); err != nil {
			h.log.Warn("create GRN line failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "inventory", g.ID, "goods_receipt.created", map[string]any{
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
	po, err := h.orm.PurchaseOrder.Query().Where(entpo.ID(g.PurchaseOrderID), entpo.TenantID(tenantID)).WithSupplier().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if !h.gateApproval(w, r, tenantID, "goods_receipt", g.ID, g.GrnNumber, po.TotalAmount) {
		return
	}
	if _, err := h.postGoodsReceiptCore(r.Context(), tenantID, g, po); err != nil {
		h.log.Error("post goods receipt failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to post goods receipt")
		return
	}

	lines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).All(r.Context())
	writeJSON(w, http.StatusOK, grnToDTO(g, lines))
}

// postGoodsReceiptCore posts an already-created DRAFT goods receipt: stock-in each accepted
// line (applyStockIn — same low-stock/recipe-reenable cascade as any other stock-in), create
// FIFO/FEFO lot layers + register serials, advance the originating PO's line
// quantity_received, recompute the PO's status (partially_received vs received), accrue
// supplier rebate, and publish goods_receipt.posted (+ purchase_order.received on full
// receipt) — the events treasury consumes to post the vendor bill / run auto-payout. Shared
// by the manual multi-line GRN-post endpoint above and ReceivePurchaseOrder's auto-generated
// "receive everything now" GRN, so BOTH paths record a real goods receipt and post to the
// ledger identically — no divergent, ad-hoc stock-bump-only path.
func (h *InventoryExtrasHandler) postGoodsReceiptCore(ctx context.Context, tenantID uuid.UUID, g *ent.GoodsReceipt, po *ent.PurchaseOrder) (fully bool, err error) {
	grnLines, _ := h.orm.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).All(ctx)

	var warehouseID uuid.UUID
	if po.WarehouseID != nil {
		warehouseID = *po.WarehouseID
	}
	if g.WarehouseID != nil {
		warehouseID = *g.WarehouseID
	}

	// Lot-tracking + shelf-life per received item, so we can create FIFO/FEFO lot layers on post.
	lotInfo := map[uuid.UUID]struct {
		track bool
		shelf *int
	}{}
	{
		ids := make([]uuid.UUID, 0, len(grnLines))
		for _, l := range grnLines {
			ids = append(ids, l.ItemID)
		}
		if items, e := h.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.IDIn(ids...)).
			Select(entitem.FieldID, entitem.FieldTrackLots, entitem.FieldIsPerishable, entitem.FieldShelfLifeDays).
			All(ctx); e == nil {
			for _, it := range items {
				lotInfo[it.ID] = struct {
					track bool
					shelf *int
				}{track: it.TrackLots || it.IsPerishable, shelf: it.ShelfLifeDays}
			}
		}
	}

	tx, err := h.orm.Tx(ctx)
	if err != nil {
		return false, err
	}
	for _, l := range grnLines {
		if err = h.applyStockIn(ctx, tx, tenantID, warehouseID, l.ItemID, l.QuantityAccepted); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		// Create a FIFO/FEFO lot layer for lot-tracked / perishable items (or when a lot number
		// was captured), so consumeLots has procurement layers + costing. Expiry seeds from the
		// line, else from the item's shelf_life_days.
		if info := lotInfo[l.ItemID]; (info.track || strings.TrimSpace(l.LotNumber) != "") && l.QuantityAccepted > 0 {
			lotNo := strings.TrimSpace(l.LotNumber)
			if lotNo == "" {
				lotNo = g.GrnNumber + "-" + l.ItemID.String()[:8]
			}
			lc := tx.InventoryLot.Create().
				SetTenantID(tenantID).SetItemID(l.ItemID).SetWarehouseID(warehouseID).
				SetLotNumber(lotNo).SetQuantity(l.QuantityAccepted).SetStatus("active").
				SetSupplierReference(g.GrnNumber)
			if l.UnitCost > 0 {
				lc = lc.SetCostPrice(l.UnitCost)
			}
			if l.ExpiryDate != nil {
				lc = lc.SetExpiryDate(*l.ExpiryDate)
			} else if info.shelf != nil && *info.shelf > 0 {
				lc = lc.SetExpiryDate(time.Now().AddDate(0, 0, *info.shelf))
			}
			if _, e := lc.Save(ctx); e != nil {
				h.log.Warn("create inventory lot on GRN failed", zap.String("lot", lotNo), zap.Error(e))
			}
		}
		// Register each captured serial as an available unit. Idempotent on re-post via the
		// unique (tenant,item,serial) index — duplicates are skipped, not fatal.
		for _, sn := range l.Serials {
			lineID := l.ID
			if _, e := tx.InventorySerial.Create().
				SetTenantID(tenantID).SetItemID(l.ItemID).SetWarehouseID(warehouseID).
				SetSerialNumber(sn).SetStatus("available").SetGoodsReceiptLineID(lineID).
				Save(ctx); e != nil {
				h.log.Warn("register inventory serial failed", zap.String("serial", sn), zap.Error(e))
			}
		}
		if l.PurchaseOrderLineID != nil {
			if _, err = tx.PurchaseOrderLine.UpdateOneID(*l.PurchaseOrderLineID).AddQuantityReceived(l.QuantityAccepted).Save(ctx); err != nil {
				_ = tx.Rollback()
				return false, err
			}
		}
	}
	if _, err = tx.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusPosted).Save(ctx); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}

	// Recompute PO status from line receipts (fully vs partially received).
	poLines, _ := h.orm.PurchaseOrderLine.Query().Where(entpoline.PoID(po.ID)).All(ctx)
	fully = len(poLines) > 0
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
		_, _ = h.orm.PurchaseOrder.UpdateOneID(po.ID).SetStatus(entpo.Status(newStatus)).Save(ctx)
	}

	// Accrue supplier rebate from the PO lines' rebate_percent on the quantity received so far.
	totalRebate := 0.0
	for _, pl := range poLines {
		if pl.RebatePercent > 0 {
			totalRebate += roundDecimal(pl.QuantityReceived * pl.UnitPrice * pl.RebatePercent / 100.0)
		}
	}
	totalRebate = roundDecimal(totalRebate)

	// Enriched goods_receipt.posted payload: treasury's per-GR vendor-bill subscriber needs
	// goods_receipt_id/gr_number/po_id/supplier_*/received_amount + per-GR lines (it was getting
	// none of these, so per-GR billing silently no-op'd). Unit price comes from the matching PO line.
	unitPriceByItem := make(map[uuid.UUID]float64, len(poLines))
	for _, pl := range poLines {
		unitPriceByItem[pl.ItemID] = pl.UnitPrice
	}
	grItemIDs := make([]uuid.UUID, 0, len(grnLines))
	for _, l := range grnLines {
		grItemIDs = append(grItemIDs, l.ItemID)
	}
	grNames, grSkus := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	if items, e := h.orm.Item.Query().Where(entitem.IDIn(grItemIDs...)).All(ctx); e == nil {
		for _, it := range items {
			grNames[it.ID] = it.Name
			grSkus[it.ID] = it.Sku
		}
	}
	grLineArr := make([]map[string]any, 0, len(grnLines))
	receivedAmount := 0.0
	for _, l := range grnLines {
		up := unitPriceByItem[l.ItemID]
		receivedAmount += l.QuantityAccepted * up
		grLineArr = append(grLineArr, map[string]any{
			"item_id":           l.ItemID,
			"sku":               grSkus[l.ItemID],
			"name":              grNames[l.ItemID],
			"quantity_received": l.QuantityAccepted,
			"unit_price":        up,
		})
	}
	grPayload := map[string]any{
		"tenant_id":            tenantID,
		"goods_receipt_id":     g.ID.String(),
		"id":                   g.ID, // back-compat
		"gr_number":            g.GrnNumber,
		"grn_number":           g.GrnNumber, // back-compat
		"po_id":                po.ID.String(),
		"purchase_order_id":    po.ID, // back-compat
		"po_number":            po.PoNumber,
		"po_status":            newStatus,
		"supplier_id":          po.SupplierID.String(),
		"currency":             po.Currency,
		"received_amount":      receivedAmount,
		"total_rebate_accrued": totalRebate,
		"pay_term_days":        derefInt(po.PayTermDays),
		"lines":                grLineArr,
	}
	// Carry the PO's project so treasury attributes the per-GR vendor bill to that project.
	if po.ProjectID != nil {
		grPayload["project_id"] = po.ProjectID
	}
	for k, v := range supplierPaymentFields(po) {
		grPayload[k] = v
	}
	h.publishOutbox(ctx, tenantID, "inventory", g.ID, "goods_receipt.posted", grPayload)
	// On full receipt, emit the enriched purchase_order.received event for treasury auto-payout.
	if fully {
		h.emitPurchaseOrderReceived(tenantID, po, totalRebate)
	}

	return fully, nil
}

// derefInt returns the pointed-to int, or 0 when nil (for event payloads where treasury defaults).
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// derefUUID returns the pointed-to UUID, or uuid.Nil when nil. Used for PO DTOs now that
// supplier_id/warehouse_id are optional (e.g. unassigned procure-to-order draft POs).
func derefUUID(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

// supplierPaymentFields returns the supplier contact/payment fields treasury needs on procurement
// events (vendor bill + auto-payout). Shared by purchase_order.received and goods_receipt.posted so
// both carry identical supplier data. Requires po loaded WithSupplier(); empty map otherwise.
func supplierPaymentFields(po *ent.PurchaseOrder) map[string]any {
	m := map[string]any{}
	if po == nil || po.Edges.Supplier == nil {
		return m
	}
	s := po.Edges.Supplier
	m["supplier_name"] = s.Name
	m["supplier_contact_email"] = s.ContactEmail
	m["supplier_contact_phone"] = s.ContactPhone
	m["supplier_payment_method"] = s.PaymentMethodType
	m["supplier_mpesa_phone"] = s.MpesaPhone
	m["supplier_mpesa_business_name"] = s.MpesaBusinessName
	m["supplier_bank_account_number"] = s.BankAccountNumber
	m["supplier_bank_name"] = s.BankName
	m["supplier_tax_pin"] = s.TaxPin
	m["supplier_paystack_recipient_code"] = s.PaystackRecipientCode
	m["requires_invoice_before_payment"] = s.RequiresInvoiceBeforePayment
	m["auto_pay_enabled"] = s.AutoPayEnabled
	return m
}

// emitPurchaseOrderReceived writes the enriched purchase_order.received outbox
// event (supplier payment details) that treasury consumes for auto-payout.
func (h *InventoryExtrasHandler) emitPurchaseOrderReceived(tenantID uuid.UUID, po *ent.PurchaseOrder, totalRebate float64) {
	payload := map[string]any{
		"po_id": po.ID, "po_number": po.PoNumber, "tenant_id": tenantID,
		"supplier_id": po.SupplierID, "total_amount": po.TotalAmount, "currency": po.Currency,
		"total_rebate_accrued":        totalRebate,
		"pay_term_days":               derefInt(po.PayTermDays),
		"additional_shipping_charges": po.AdditionalShippingCharges,
	}
	// project_id (when the PO came from a project requisition) lets treasury attribute the cost
	// to the project's budget actuals.
	if po.ProjectID != nil {
		payload["project_id"] = po.ProjectID
	}
	for k, v := range supplierPaymentFields(po) {
		payload[k] = v
	}

	// Itemize the receipt so treasury can build a line-level vendor bill: one entry per PO line
	// with the item's name/sku, received quantity and unit price.
	if poLines, err := h.orm.PurchaseOrderLine.Query().Where(entpoline.PoID(po.ID)).All(context.Background()); err == nil && len(poLines) > 0 {
		itemIDs := make([]uuid.UUID, 0, len(poLines))
		for _, l := range poLines {
			itemIDs = append(itemIDs, l.ItemID)
		}
		names := map[uuid.UUID]string{}
		skus := map[uuid.UUID]string{}
		if items, e := h.orm.Item.Query().Where(entitem.IDIn(itemIDs...)).All(context.Background()); e == nil {
			for _, it := range items {
				names[it.ID] = it.Name
				skus[it.ID] = it.Sku
			}
		}
		lineArr := make([]map[string]any, 0, len(poLines))
		for _, l := range poLines {
			qty := l.QuantityReceived
			if qty <= 0 {
				qty = l.QuantityOrdered
			}
			lineArr = append(lineArr, map[string]any{
				"item_id":    l.ItemID,
				"sku":        skus[l.ItemID],
				"name":       names[l.ItemID],
				"quantity":   qty,
				"unit_price": l.UnitPrice,
			})
		}
		payload["lines"] = lineArr
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
