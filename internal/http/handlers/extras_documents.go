package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/documents"

	invent "github.com/bengobox/inventory-service/internal/ent"
	entapprovalaction "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpodoc "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpolinedoc "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entsupplierdoc "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwhdoc "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// GeneratePurchaseOrderPDF renders a branded PO PDF (GET /inventory/purchase-orders/{poID}/pdf).
//
//	@Summary      Generate a branded purchase order PDF
//	@Tags         Procurement
//	@Produce      application/pdf
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {file}    binary
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/pdf [get]
func (h *InventoryExtrasHandler) GeneratePurchaseOrderPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
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
	ctx := r.Context()
	po, err := h.orm.PurchaseOrder.Query().Where(entpodoc.ID(poID), entpodoc.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	lines, _ := h.orm.PurchaseOrderLine.Query().Where(entpolinedoc.PoID(po.ID)).All(ctx)

	supplierName := ""
	var supplierAddr []string
	if sup, e := h.orm.Supplier.Query().Where(entsupplierdoc.ID(po.SupplierID)).Only(ctx); e == nil {
		supplierName = sup.Name
		if sup.Address != "" {
			supplierAddr = strings.Split(sup.Address, "\n")
		}
	}
	warehouseName := ""
	if wh, e := h.orm.Warehouse.Query().Where(entwhdoc.ID(po.WarehouseID)).Only(ctx); e == nil {
		warehouseName = wh.Name
	}

	items := make([]documents.DocLine, 0, len(lines))
	var subtotal float64
	for _, l := range lines {
		desc := l.ItemID.String()
		if it, e := h.orm.Item.Query().Where(entitem.ID(l.ItemID)).Only(ctx); e == nil {
			desc = it.Name
		}
		subtotal += l.TotalPrice
		items = append(items, documents.DocLine{
			Desc:   desc,
			Qty:    fmt.Sprintf("%g", l.QuantityOrdered),
			Rate:   formatMoney(l.UnitPrice),
			Amount: formatMoney(l.TotalPrice),
		})
	}
	tax := po.TotalAmount - subtotal
	taxLabel, taxAmount := "", ""
	if tax > 0.0049 {
		taxLabel, taxAmount = "Tax", formatMoney(tax)
	}

	doc := documents.PurchaseOrderDoc{
		Branding:      h.docSvc.GetBranding(ctx, tenantID),
		PONumber:      po.PoNumber,
		Date:          po.CreatedAt.Format("02 January 2006"),
		Currency:      po.Currency,
		Status:        string(po.Status),
		SupplierName:  supplierName,
		SupplierAddr:  supplierAddr,
		WarehouseName: warehouseName,
		Items:         items,
		Subtotal:      formatMoney(subtotal),
		TaxLabel:      taxLabel,
		TaxAmount:     taxAmount,
		Grand:         formatMoney(po.TotalAmount),
		Notes:         []string{"Please quote PO number " + po.PoNumber + " on all correspondence and invoices."},
	}
	if po.ExpectedDate != nil {
		doc.ExpectedDate = po.ExpectedDate.Format("02 January 2006")
	}

	// Prepared/Approved capture. PreparedBy = the PO creator. ApprovedBy is set ONLY when the PO
	// actually passed an approval (per the approval rules/steps) — naming the final approver — so
	// documents that never required approval don't show an empty "Approved By" slot.
	doc.PreparedBy = h.resolveUserLabel(ctx, tenantID, po.CreatedBy, "")
	if appr, _ := h.approvals().Latest(ctx, tenantID, po.ID); appr != nil && string(appr.Status) == "approved" {
		if act, e := h.orm.ApprovalAction.Query().
			Where(entapprovalaction.RequestID(appr.ID), entapprovalaction.StatusEQ(entapprovalaction.StatusApproved)).
			Order(invent.Desc(entapprovalaction.FieldSequence)).First(ctx); e == nil {
			doc.ApprovedBy = h.resolveUserLabel(ctx, tenantID, act.ActedBy, act.ApproverRole)
		}
	}

	pdfBytes, err := documents.RenderPurchaseOrderPDF(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render purchase order PDF")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.pdf"`, po.PoNumber))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// resolveUserLabel maps an auth-service user id to a human label for the signature lines:
// the local InventoryUser's full name when known, else their email, else the supplied fallback
// (e.g. the approver role) when the user can't be resolved or no id is set.
func (h *InventoryExtrasHandler) resolveUserLabel(ctx context.Context, tenantID uuid.UUID, userID *uuid.UUID, fallback string) string {
	if userID == nil || *userID == uuid.Nil {
		return fallback
	}
	if u, e := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(*userID)).
		Only(ctx); e == nil {
		if u.Name != "" {
			return u.Name
		}
		if u.Email != "" {
			return u.Email
		}
	}
	return fallback
}

// formatMoney formats a float with thousands separators and 2 decimals (e.g. 26,540.70).
func formatMoney(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%.2f", v)
	dot := strings.IndexByte(s, '.')
	intPart, frac := s[:dot], s[dot:]
	var b strings.Builder
	n := len(intPart)
	for i, c := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}
