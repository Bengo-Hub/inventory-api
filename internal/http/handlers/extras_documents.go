package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/documents"

	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpodoc "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpolinedoc "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entsupplierdoc "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwhdoc "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// GeneratePurchaseOrderPDF renders a branded PO PDF (GET /inventory/purchase-orders/{poID}/pdf).
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
			Qty:    fmt.Sprintf("%d", l.QuantityOrdered),
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
