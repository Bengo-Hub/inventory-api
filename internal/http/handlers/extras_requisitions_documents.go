package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entreqline "github.com/bengobox/inventory-service/internal/ent/requisitionline"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GenerateRequisitionPDF renders a branded requisition PDF
// (GET /inventory/requisitions/{reqID}/pdf). Reuses the requisition's already-minted
// reference_number (issued at creation via documents.DocTypeRequisition) — no numbering here.
//
//	@Summary      Generate a branded requisition PDF
//	@Tags         Procurement
//	@Produce      application/pdf
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {file}    binary
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Failure      500    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/pdf [get]
func (h *InventoryExtrasHandler) GenerateRequisitionPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	lines, _ := h.orm.RequisitionLine.Query().
		Where(entreqline.RequisitionID(rq.ID), entreqline.TenantID(tenantID)).All(ctx)

	itemCache := map[uuid.UUID]string{}
	items := make([]documents.RequisitionDocLine, 0, len(lines))
	var estimated float64
	for _, l := range lines {
		desc := l.Description
		if l.ItemID != nil {
			if n := h.itemName(ctx, tenantID, *l.ItemID, itemCache); n != "" {
				desc = n
			}
		}
		if desc == "" {
			desc = ifEmptyStr(l.ServiceDescription, "(unspecified)")
		}
		line := documents.RequisitionDocLine{
			Desc:     desc,
			SubDesc:  l.Specifications,
			ItemType: string(l.ItemType),
			Qty:      formatQty(l.Quantity),
			Urgent:   l.Urgent,
		}
		if l.ApprovedQuantity != nil {
			line.ApprovedQty = formatQty(*l.ApprovedQuantity)
		}
		if l.EstimatedPrice != nil && *l.EstimatedPrice > 0 {
			// The requisition's estimate is a UNIT price; the line estimate is the requested
			// (or, once decided, the approved) quantity at that price.
			qty := l.Quantity
			if l.ApprovedQuantity != nil {
				qty = *l.ApprovedQuantity
			}
			lineTotal := *l.EstimatedPrice * qty
			estimated += lineTotal
			line.UnitEstimate = formatMoney(*l.EstimatedPrice)
			line.LineEstimate = formatMoney(lineTotal)
		}
		items = append(items, line)
	}

	doc := documents.RequisitionDoc{
		Branding:        h.docSvc.GetBranding(ctx, tenantID),
		ReferenceNumber: rq.ReferenceNumber,
		Date:            rq.CreatedAt.Format("02 January 2006"),
		Status:          string(rq.Status),
		RequestType:     string(rq.RequestType),
		Priority:        string(rq.Priority),
		Currency:        "KES",
		Purpose:         rq.Purpose,
		RequesterName:   h.resolveUserLabel(ctx, tenantID, rq.RequesterID, ""),
		OutletName:      h.outletWarehouseName(ctx, tenantID, rq.OutletID),
		Items:           items,
		Notes:           nonEmptyStrs(rq.Notes),
	}
	if rq.RequiredByDate != nil {
		doc.RequiredByDate = rq.RequiredByDate.Format("02 January 2006")
	}
	if estimated > 0.0049 {
		doc.EstimatedTotal = formatMoney(estimated)
	}
	doc.PreparedBy = doc.RequesterName
	// Approved By is captured only when the requisition actually passed approval, so a draft
	// never shows an empty approver slot.
	if appr, _ := h.approvals().Latest(ctx, tenantID, rq.ID); appr != nil && string(appr.Status) == "approved" {
		doc.ApprovedBy = h.latestApproverLabel(ctx, tenantID, appr.ID)
	}

	pdfBytes, err := documents.RenderRequisitionPDF(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render requisition PDF")
		return
	}
	writePDF(w, rq.ReferenceNumber, pdfBytes)
}

// outletWarehouseName resolves a human label for an outlet id by naming the warehouse that
// serves it — inventory-api holds no outlet entity of its own. Empty when nothing resolves.
func (h *InventoryExtrasHandler) outletWarehouseName(ctx context.Context, tenantID uuid.UUID, outletID *uuid.UUID) string {
	if outletID == nil || *outletID == uuid.Nil {
		return ""
	}
	if wh, e := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletIDEQ(*outletID)).
		First(ctx); e == nil {
		return wh.Name
	}
	return ""
}

// itemSKU resolves an item's SKU for a document sub-line, using the same tenant-checked
// single-fetch + cache shape as itemName.
func (h *InventoryExtrasHandler) itemSKU(ctx context.Context, tenantID, id uuid.UUID, cache map[uuid.UUID]string) string {
	if cache != nil {
		if s, ok := cache[id]; ok {
			return s
		}
	}
	sku := ""
	if it, err := h.orm.Item.Query().
		Where(entitem.ID(id), entitem.TenantID(tenantID)).Only(ctx); err == nil {
		sku = it.Sku
	}
	if cache != nil {
		cache[id] = sku
	}
	return sku
}

// writePDF writes rendered PDF bytes with the inline-disposition headers every document
// endpoint in this service uses (matching GeneratePurchaseOrderPDF).
func writePDF(w http.ResponseWriter, filename string, body []byte) {
	if filename == "" {
		filename = "document"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.pdf"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
