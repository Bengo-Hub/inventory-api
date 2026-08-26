package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// stockHistoryDocExportCap bounds a document export to a sane number of printed rows — a stock
// ledger export is a point-in-time audit snapshot, not a paginated screen, but an unbounded item
// spanning years of history must not be allowed to hang the renderer.
const stockHistoryDocExportCap = 2000

// GenerateStockHistoryDocument renders the Stock History page's current view (same
// warehouse/date/type filters as the on-screen ledger) as a branded PDF/XLSX/CSV export
// (GET /inventory/items/{sku}/stock-history/document).
//
//	@Summary      Export a product's stock history
//	@Tags         stock
//	@Produce      application/pdf
//	@Param        sku           path      string  true   "Item SKU"
//	@Param        format        query     string  false  "pdf (default) | csv | xlsx"
//	@Param        warehouse_id  query     string  false  "Scope to one warehouse"
//	@Param        date_from     query     string  false  "RFC3339 or YYYY-MM-DD lower bound"
//	@Param        date_to       query     string  false  "RFC3339 or YYYY-MM-DD upper bound"
//	@Param        type          query     string  false  "Comma-separated movement types"
//	@Success      200           {file}    binary
//	@Failure      404           {object}  map[string]string
//	@Failure      500           {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/items/{sku}/stock-history/document [get]
func (h *InventoryHandler) GenerateStockHistoryDocument(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	format, ok := docFormatFromQuery(w, r)
	if !ok {
		return
	}
	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	ctx := r.Context()

	f := stock.StockHistoryFilter{Limit: stockHistoryDocExportCap}
	scopeParts := make([]string, 0, 3)
	if whStr := r.URL.Query().Get("warehouse_id"); whStr != "" {
		if whID, pErr := uuid.Parse(whStr); pErr == nil {
			f.WarehouseID = &whID
			if wh, wErr := h.orm.Warehouse.Get(ctx, whID); wErr == nil {
				scopeParts = append(scopeParts, wh.Name)
			}
		}
	}
	if t, ok := parseHistoryDate(r.URL.Query().Get("date_from"), false); ok {
		f.DateFrom = &t
	}
	if t, ok := parseHistoryDate(r.URL.Query().Get("date_to"), true); ok {
		f.DateTo = &t
	}
	if f.DateFrom != nil || f.DateTo != nil {
		from, to := "…", "…"
		if f.DateFrom != nil {
			from = f.DateFrom.Format("02 Jan 2006")
		}
		if f.DateTo != nil {
			to = f.DateTo.Format("02 Jan 2006")
		}
		scopeParts = append(scopeParts, from+" – "+to)
	}
	if typesParam := strings.TrimSpace(r.URL.Query().Get("type")); typesParam != "" {
		f.Types = strings.Split(typesParam, ",")
		scopeParts = append(scopeParts, strings.Join(f.Types, ", "))
	}

	res, err := h.stockSvc.ItemStockHistory(ctx, tenantID, sku, f)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("stock history document failed", zap.String("sku", sku), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "STOCK_HISTORY_FAILED", "Failed to build stock history")
		return
	}

	unit := res.Item.UnitAbbreviation
	qty := func(n float64) string {
		s := fmt.Sprintf("%.4g", n)
		if unit != "" {
			s += " " + unit
		}
		return s
	}

	items := make([]documents.StockHistoryDocLine, 0, len(res.Movements))
	for _, mv := range res.Movements {
		user := mv.ActorName
		if user == "" {
			user = "—"
		}
		counterparty := mv.Counterparty
		if counterparty == "" {
			counterparty = "—"
		}
		items = append(items, documents.StockHistoryDocLine{
			Date:           mv.OccurredAt.Format("02 Jan 2006 15:04"),
			Type:           mv.Label,
			Reference:      ifEmptyStr(mv.Reference, "—"),
			Location:       ifEmptyStr(mv.WarehouseName, "—"),
			Counterparty:   counterparty,
			User:           user,
			QuantityChange: signedQty(mv.QuantityChange),
		})
	}

	doc := documents.StockHistoryDoc{
		Branding: h.docSvc.GetBranding(ctx, tenantID),
		ItemName: res.Item.Name,
		ItemSKU:  res.Item.Sku,
		Scope:    strings.Join(scopeParts, " · "),
		Summary: [][2]string{
			{"Opening Stock", qty(res.Summary.OpeningStock)},
			{"Total Purchased", qty(res.Summary.TotalPurchased)},
			{"Total Sold", qty(res.Summary.TotalSold)},
			{"Total Sell Returns", qty(res.Summary.TotalSellReturns)},
			{"Total Purchase Returns", qty(res.Summary.TotalPurchaseReturns)},
			{"Transfers In", qty(res.Summary.TransfersIn)},
			{"Transfers Out", qty(res.Summary.TransfersOut)},
			{"Net Adjustments", qty(res.Summary.TotalAdjusted)},
			{"Current Stock", qty(res.Summary.CurrentStock)},
		},
		Items: items,
	}

	var fileBytes []byte
	switch format {
	case "csv":
		fileBytes, err = documents.RenderStockHistoryCSV(doc)
	case "xlsx":
		fileBytes, err = documents.RenderStockHistoryXLSX(doc)
	default:
		fileBytes, err = documents.RenderStockHistoryPDF(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOC_RENDER_FAILED", "Failed to render stock history document")
		return
	}
	fname := "stock-history-" + res.Item.Sku + "-" + time.Now().Format("20060102")
	writeDocFile(w, fname, format, fileBytes)
}
