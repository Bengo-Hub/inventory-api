package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
)

// ItemStockHistory handles GET /v1/{tenant}/inventory/items/{sku}/stock-history —
// the Go-Digital-style per-item ledger: quantities-in/out summary cards + a
// unified movement table (adjustments, purchases, sales, returns, transfers).
//
//	@Summary      Product stock history
//	@Description  Per-item summary (opening stock, purchases, sold, sell/purchase returns, transfers in/out, net adjustments, current stock) plus a unified paginated movement ledger. Optional warehouse and date-range scoping.
//	@Tags         stock
//	@Produce      json
//	@Param        tenant        path      string  true   "Tenant ID"
//	@Param        sku           path      string  true   "Item SKU"
//	@Param        warehouse_id  query     string  false  "Scope to one warehouse"
//	@Param        date_from     query     string  false  "RFC3339 or YYYY-MM-DD lower bound"
//	@Param        date_to       query     string  false  "RFC3339 or YYYY-MM-DD upper bound"
//	@Success      200           {object}  stock.StockHistoryResult
//	@Failure      404           {object}  map[string]string
//	@Router       /{tenant}/inventory/items/{sku}/stock-history [get]
func (h *InventoryHandler) ItemStockHistory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	f := stock.StockHistoryFilter{}
	if whStr := r.URL.Query().Get("warehouse_id"); whStr != "" {
		if whID, pErr := uuid.Parse(whStr); pErr == nil {
			f.WarehouseID = &whID
		}
	}
	if t, ok := parseHistoryDate(r.URL.Query().Get("date_from"), false); ok {
		f.DateFrom = &t
	}
	if t, ok := parseHistoryDate(r.URL.Query().Get("date_to"), true); ok {
		f.DateTo = &t
	}
	p := pagination.Parse(r)
	f.Limit = p.Limit
	f.Offset = p.Offset

	res, err := h.stockSvc.ItemStockHistory(r.Context(), tenantID, sku, f)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("item stock history failed", zap.String("sku", sku), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "STOCK_HISTORY_FAILED", "Failed to build stock history")
		return
	}
	// Standard pagination envelope + the item/summary header the modal renders.
	writeJSON(w, http.StatusOK, map[string]any{
		"item":    res.Item,
		"summary": res.Summary,
		"data":    res.Movements,
		"total":   res.Total,
		"limit":   p.Limit,
		"page":    p.Page,
		"hasMore": p.Offset+len(res.Movements) < res.Total,
	})
}

// parseHistoryDate accepts RFC3339 or bare YYYY-MM-DD; endOfDay pushes a bare
// date to 23:59:59 so date_to is inclusive.
func parseHistoryDate(s string, endOfDay bool) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t, true
	}
	return time.Time{}, false
}
