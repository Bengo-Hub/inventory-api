package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
)

// GenerateDocument handles GET /v1/{tenant}/inventory/transfers/{transferId}/pdf?type=delivery_note|grn
// — renders the transfer's Dispatch/Transit Note (once shipped) or Goods-Received Note (once
// received). Both reuse the transfer's own already-issued transfer_number; no separate document
// sequence. type defaults to whichever document the transfer's CURRENT status supports.
func (h *TransferHandler) GenerateDocument(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	transferID, err := uuid.Parse(chi.URLParam(r, "transferId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid transfer ID")
		return
	}

	transfer, err := h.transferSvc.GetTransfer(r.Context(), tenantID, transferID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	docType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if docType == "" {
		// Default to whichever document the CURRENT status actually supports — a received
		// transfer's natural document is the GRN, everything else falls back to the dispatch note.
		if transfer.Status == "received" {
			docType = "grn"
		} else {
			docType = "delivery_note"
		}
	}

	var doc documents.TransferDoc
	switch docType {
	case "delivery_note":
		if transfer.Status != "in_transit" && transfer.Status != "received" {
			writeError(w, http.StatusConflict, "NOT_SHIPPED", "The delivery/transit note is only available once the transfer has shipped")
			return
		}
		doc = deliveryNoteFromTransfer(transfer)
	case "grn":
		if transfer.Status != "received" {
			writeError(w, http.StatusConflict, "NOT_RECEIVED", "The goods-received note is only available once the transfer has been received")
			return
		}
		doc = goodsReceivedNoteFromTransfer(transfer)
	default:
		writeError(w, http.StatusBadRequest, "INVALID_TYPE", `type must be "delivery_note" or "grn"`)
		return
	}
	doc.Branding = h.docSvc.GetBranding(r.Context(), tenantID)

	pdfBytes, err := documents.RenderTransferPDF(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render transfer document")
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s-%s.pdf"`, transfer.TransferNumber, docType))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// deliveryNoteFromTransfer builds the ship-time Dispatch/Transit Note — no received/variance
// columns (nothing has been confirmed at the destination yet), "Dispatched By"/"Received By"
// sign-off (the right side is filled in only once actually received, so it renders blank here).
func deliveryNoteFromTransfer(t *transfers.TransferResponse) documents.TransferDoc {
	items := make([]documents.TransferDocLine, len(t.Lines))
	for i, l := range t.Lines {
		items[i] = documents.TransferDocLine{
			Desc:    ifEmptyStr(l.ItemName, l.ItemID.String()),
			SubDesc: l.ItemSKU,
			Qty:     formatQty(l.Quantity),
		}
	}
	date := ""
	if t.ShippedAt != nil {
		date = t.ShippedAt.Format("02 January 2006")
	}
	return documents.TransferDoc{
		DocTitle:            "Delivery Note",
		DocSubtitle:         "Transit Document",
		Status:              t.Status,
		TransferNumber:      t.TransferNumber,
		Date:                date,
		Reference:           t.ReferenceNo,
		Carrier:             t.Carrier,
		FromWarehouseName:   t.SourceWarehouse.Name,
		FromWarehouseAddr:   addrLines(t.SourceWarehouse.Address),
		ToWarehouseName:     t.DestinationWarehouse.Name,
		ToWarehouseAddr:     addrLines(t.DestinationWarehouse.Address),
		Items:               items,
		AcknowledgementText: "Please inspect the goods on arrival and sign below to confirm receipt.",
		Notes:               nonEmptyStrs(t.Notes, t.FreightNotes),
		LeftSigLabel:        "Dispatched By",
		RightSigLabel:       "Received By",
	}
}

// goodsReceivedNoteFromTransfer builds the receive-time confirmation — gains a RECEIVED +
// VARIANCE column per line (drawTransferItems only shows them when at least one line carries a
// ReceivedQty) and a "Delivered By"/"Received By" sign-off.
func goodsReceivedNoteFromTransfer(t *transfers.TransferResponse) documents.TransferDoc {
	items := make([]documents.TransferDocLine, len(t.Lines))
	for i, l := range t.Lines {
		received := l.Quantity
		if l.ReceivedQty != nil {
			received = *l.ReceivedQty
		}
		items[i] = documents.TransferDocLine{
			Desc:           ifEmptyStr(l.ItemName, l.ItemID.String()),
			SubDesc:        l.ItemSKU,
			Qty:            formatQty(l.Quantity),
			ReceivedQty:    formatQty(received),
			VarianceReason: l.VarianceReason,
		}
	}
	date := ""
	if t.ReceivedAt != nil {
		date = t.ReceivedAt.Format("02 January 2006")
	}
	return documents.TransferDoc{
		DocTitle:            "Goods Received Note",
		DocSubtitle:         "Receipt Confirmation",
		Status:              t.Status,
		TransferNumber:      t.TransferNumber,
		Date:                date,
		Reference:           t.ReferenceNo,
		Carrier:             t.Carrier,
		FromWarehouseName:   t.SourceWarehouse.Name,
		FromWarehouseAddr:   addrLines(t.SourceWarehouse.Address),
		ToWarehouseName:     t.DestinationWarehouse.Name,
		ToWarehouseAddr:     addrLines(t.DestinationWarehouse.Address),
		Items:               items,
		AcknowledgementText: fmt.Sprintf("Received the following goods into %s in good order and condition:", ifEmptyStr(t.DestinationWarehouse.Name, "the destination warehouse")),
		Notes:               nonEmptyStrs(t.Notes, t.FreightNotes),
		LeftSigLabel:        "Delivered By",
		RightSigLabel:       "Received By",
	}
}

func ifEmptyStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func formatQty(q float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", q), "0"), ".")
}

func addrLines(addr string) []string {
	if strings.TrimSpace(addr) == "" {
		return nil
	}
	return strings.Split(addr, "\n")
}

func nonEmptyStrs(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
