package handlers

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// CreateBreakdown handles POST /v1/{tenant}/inventory/breakdowns — a bulk-to-retail-unit stock
// conversion (e.g. break a crate into bottles). Decrements the parent SKU and increments the
// child SKU atomically, with audit adjustments and an inventory.stock.broken_down event.
func (h *InventoryHandler) CreateBreakdown(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.BreakdownRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.ParentSKU == "" || req.ChildSKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "parent_sku and child_sku are required")
		return
	}

	result, err := h.stockSvc.Breakdown(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("stock breakdown failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "BREAKDOWN_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
