package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GenerateRFQPDF renders a branded request-for-quotation PDF (GET /inventory/rfqs/{rfqID}/pdf).
// Reuses the RFQ's already-minted rfq_number (issued at creation via documents.DocTypeRFQ).
//
// Supplier responses are rendered as an APPENDIX when present and skipped gracefully when not —
// the base RFQ (the document actually sent out to be quoted on) must never depend on them.
//
//	@Summary      Generate a branded request-for-quotation PDF
//	@Tags         Procurement
//	@Produce      application/pdf
//	@Param        rfqID  path      string  true  "RFQ ID"
//	@Success      200    {file}    binary
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Failure      500    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/rfqs/{rfqID}/pdf [get]
func (h *InventoryExtrasHandler) GenerateRFQPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	format, ok := docFormatFromQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	full, err := h.loadRFQFull(ctx, tenantID, rfqID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}

	itemCache, skuCache, supCache := map[uuid.UUID]string{}, map[uuid.UUID]string{}, map[uuid.UUID]string{}
	items := make([]documents.RFQDocLine, 0, len(full.Edges.Lines))
	for _, l := range full.Edges.Lines {
		desc, sub := l.Description, ""
		if l.ItemID != nil {
			if n := h.itemName(ctx, tenantID, *l.ItemID, itemCache); n != "" {
				desc = n
			}
			sub = h.itemSKU(ctx, tenantID, *l.ItemID, skuCache)
		}
		items = append(items, documents.RFQDocLine{
			Desc:    ifEmptyStr(desc, "(unspecified)"),
			SubDesc: sub,
			Uom:     l.Uom,
			Qty:     formatQty(l.Quantity),
		})
	}

	// Which suppliers won an award, so the appendix doubles as the award record once decided.
	awarded := map[uuid.UUID]bool{}
	for _, a := range full.Edges.Awards {
		awarded[a.SupplierID] = true
	}
	quotes := make([]documents.RFQDocQuote, 0, len(full.Edges.Responses))
	for _, resp := range full.Edges.Responses {
		total, maxLead := 0.0, 0
		for _, qi := range resp.QuotedItems {
			total += qi.UnitPrice * h.qtyForLine(full, qi.RFQLineID)
			if qi.LeadTimeDays > maxLead {
				maxLead = qi.LeadTimeDays
			}
		}
		q := documents.RFQDocQuote{
			SupplierName: ifEmptyStr(h.supplierName(ctx, tenantID, resp.SupplierID, supCache), resp.SupplierID.String()),
			Status:       string(resp.Status),
			Currency:     resp.Currency,
			Notes:        resp.Notes,
			Awarded:      awarded[resp.SupplierID],
		}
		if total > 0.0049 {
			q.QuotedTotal = formatMoney(total)
		}
		if maxLead > 0 {
			q.LeadTime = fmt.Sprintf("%d days", maxLead)
		}
		quotes = append(quotes, q)
	}

	warehouseName := ""
	if full.WarehouseID != nil {
		if wh, e := h.orm.Warehouse.Query().Where(entwh.ID(*full.WarehouseID)).Only(ctx); e == nil {
			warehouseName = wh.Name
		}
	}
	requisitionNumber := ""
	if full.RequisitionID != nil {
		if rq, e := h.orm.Requisition.Query().
			Where(entreq.ID(*full.RequisitionID), entreq.TenantID(tenantID)).Only(ctx); e == nil {
			requisitionNumber = rq.ReferenceNumber
		}
	}

	doc := documents.RFQDoc{
		Branding:          h.docSvc.GetBranding(ctx, tenantID),
		RFQNumber:         full.RfqNumber,
		Title:             full.Title,
		Date:              full.CreatedAt.Format("02 January 2006"),
		Status:            string(full.Status),
		WarehouseName:     warehouseName,
		RequisitionNumber: requisitionNumber,
		Items:             items,
		Quotes:            quotes,
		Notes:             nonEmptyStrs(full.Notes),
		PreparedBy:        h.resolveUserLabel(ctx, tenantID, full.CreatedBy, ""),
	}
	if full.DueDate != nil {
		doc.DueDate = full.DueDate.Format("02 January 2006")
	}

	var fileBytes []byte
	switch format {
	case "csv":
		fileBytes, err = documents.RenderRFQCSV(doc)
	case "xlsx":
		fileBytes, err = documents.RenderRFQXLSX(doc)
	default:
		fileBytes, err = documents.RenderRFQPDF(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOC_RENDER_FAILED", "Failed to render RFQ document")
		return
	}
	writeDocFile(w, full.RfqNumber, format, fileBytes)
}
