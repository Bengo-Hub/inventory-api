package handlers

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GeneratePurchaseReturnPDF renders a branded supplier purchase-return (debit note) PDF
// (GET /inventory/purchase-returns/{returnID}/pdf). Reuses the return's already-minted
// return_number (issued at creation via documents.DocTypePurchaseReturn).
//
//	@Summary      Generate a branded purchase return (debit note) PDF
//	@Tags         Procurement
//	@Produce      application/pdf
//	@Param        returnID  path      string  true  "Purchase return ID"
//	@Success      200       {file}    binary
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Failure      500       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/{returnID}/pdf [get]
func (h *InventoryExtrasHandler) GeneratePurchaseReturnPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	lines, _ := h.orm.PurchaseReturnLine.Query().
		Where(entprline.PurchaseReturnID(pr.ID), entprline.TenantID(tenantID)).All(ctx)

	poNumber, currency := "", ""
	if pr.PurchaseOrderID != nil {
		if po, e := h.orm.PurchaseOrder.Query().
			Where(entpo.ID(*pr.PurchaseOrderID), entpo.TenantID(tenantID)).Only(ctx); e == nil {
			poNumber, currency = po.PoNumber, po.Currency
		}
	}
	grnNumber := ""
	if pr.GoodsReceiptID != nil {
		if g, e := h.orm.GoodsReceipt.Query().
			Where(entgr.ID(*pr.GoodsReceiptID), entgr.TenantID(tenantID)).Only(ctx); e == nil {
			grnNumber = g.GrnNumber
		}
	}

	supplierName := ""
	var supplierAddr []string
	if pr.SupplierID != nil {
		if sup, e := h.orm.Supplier.Query().Where(entsupplier.ID(*pr.SupplierID)).Only(ctx); e == nil {
			supplierName = sup.Name
			if sup.Address != "" {
				supplierAddr = strings.Split(sup.Address, "\n")
			}
		}
	}

	itemCache, skuCache := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	items := make([]documents.PurchaseReturnDocLine, 0, len(lines))
	for _, l := range lines {
		sub := h.itemSKU(ctx, tenantID, l.ItemID, skuCache)
		if l.LotID != nil {
			if lot, e := h.orm.InventoryLot.Query().Where(entlot.ID(*l.LotID)).Only(ctx); e == nil && lot.LotNumber != "" {
				sub = joinDetail(sub, "Lot "+lot.LotNumber)
			}
		}
		line := documents.PurchaseReturnDocLine{
			Desc:    ifEmptyStr(h.itemName(ctx, tenantID, l.ItemID, itemCache), l.ItemID.String()),
			SubDesc: sub,
			Qty:     formatQty(float64(l.Quantity)),
			Amount:  formatMoney(l.SubTotal),
		}
		// The entity stores only the line sub-total, so the unit price is derived (and omitted
		// entirely for a zero-quantity line rather than dividing by zero).
		if l.Quantity > 0 {
			line.UnitPrice = formatMoney(l.SubTotal / float64(l.Quantity))
		}
		items = append(items, line)
	}

	doc := documents.PurchaseReturnDoc{
		Branding:            h.docSvc.GetBranding(ctx, tenantID),
		ReturnNumber:        pr.ReturnNumber,
		Date:                pr.DateReturned.Format("02 January 2006"),
		Currency:            currency,
		PaymentStatus:       string(pr.PaymentStatus),
		Reason:              pr.Reason,
		PurchaseOrderNumber: poNumber,
		GrnNumber:           grnNumber,
		SupplierName:        supplierName,
		SupplierAddr:        supplierAddr,
		Items:               items,
		ReturnAmount:        formatMoney(pr.ReturnAmount),
		PreparedBy:          h.resolveUserLabel(ctx, tenantID, pr.AddedBy, ""),
	}
	// Only surface an outstanding-credit row when some of the claim is genuinely unsettled.
	if pr.ReturnAmountDue > 0.0049 {
		doc.AmountDue = formatMoney(pr.ReturnAmountDue)
	}
	if appr, _ := h.approvals().Latest(ctx, tenantID, pr.ID); appr != nil && string(appr.Status) == "approved" {
		doc.ApprovedBy = h.latestApproverLabel(ctx, tenantID, appr.ID)
	}

	pdfBytes, err := documents.RenderPurchaseReturnPDF(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render purchase return PDF")
		return
	}
	writePDF(w, pr.ReturnNumber, pdfBytes)
}
