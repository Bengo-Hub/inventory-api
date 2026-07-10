package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/reports"
)

// SetReportsService injects the ingredient-utilization reports service.
func (h *InventoryExtrasHandler) SetReportsService(svc *reports.Service) {
	h.reportsSvc = svc
}

// parseUtilizationRange parses the common item_id/warehouse_id/from/to query params shared
// by all three ingredient-utilization endpoints. Defaults to the trailing 30 days, matching
// the food-cost-variance report's default window.
func parseUtilizationRange(r *http.Request) (itemID, warehouseID uuid.UUID, from, to time.Time, err error) {
	itemID, err = uuid.Parse(r.URL.Query().Get("item_id"))
	if err != nil {
		return
	}
	warehouseID, err = uuid.Parse(r.URL.Query().Get("warehouse_id"))
	if err != nil {
		return
	}
	now := time.Now().UTC()
	from, to = now.AddDate(0, 0, -30), now
	if s := r.URL.Query().Get("from"); s != "" {
		if t, perr := time.Parse("2006-01-02", s); perr == nil {
			from = t
		} else if t, perr := time.Parse(time.RFC3339, s); perr == nil {
			from = t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, perr := time.Parse("2006-01-02", s); perr == nil {
			to = t
		} else if t, perr := time.Parse(time.RFC3339, s); perr == nil {
			to = t
		}
	}
	err = nil
	return
}

// IngredientUtilizationSummary handles GET /inventory/reports/ingredient-utilization/summary
//
//	@Summary  Ingredient utilization KPI tiles
//	@Tags     Reports
//	@Param    item_id       query  string  true   "Ingredient item ID"
//	@Param    warehouse_id  query  string  true   "Warehouse ID"
//	@Param    from          query  string  false  "Period start (YYYY-MM-DD), default 30 days ago"
//	@Param    to            query  string  false  "Period end (YYYY-MM-DD), default now"
//	@Success  200  {object}  reports.IngredientUtilizationSummary
//	@Router   /{tenant}/inventory/reports/ingredient-utilization/summary [get]
func (h *InventoryExtrasHandler) IngredientUtilizationSummary(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.reportsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Reports service not initialized")
		return
	}
	itemID, warehouseID, from, to, err := parseUtilizationRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "item_id and warehouse_id are required and must be valid UUIDs")
		return
	}
	result, err := h.reportsSvc.GetSummary(r.Context(), tenantID, itemID, warehouseID, from, to)
	if err != nil {
		h.log.Error("ingredient utilization summary failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute ingredient utilization summary")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// IngredientUtilizationTimeseries handles GET /inventory/reports/ingredient-utilization/timeseries
//
//	@Summary  Ingredient utilization trend, stacked by recipe
//	@Tags     Reports
//	@Param    item_id       query  string  true   "Ingredient item ID"
//	@Param    warehouse_id  query  string  true   "Warehouse ID"
//	@Param    from          query  string  false  "Period start (YYYY-MM-DD), default 30 days ago"
//	@Param    to            query  string  false  "Period end (YYYY-MM-DD), default now"
//	@Param    granularity   query  string  false  "day|week|biweek|month (default day)"
//	@Param    recipe_id     query  []string  false  "Restrict to these recipe IDs (repeatable)"
//	@Success  200  {object}  reports.TimeseriesResponse
//	@Router   /{tenant}/inventory/reports/ingredient-utilization/timeseries [get]
func (h *InventoryExtrasHandler) IngredientUtilizationTimeseries(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.reportsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Reports service not initialized")
		return
	}
	itemID, warehouseID, from, to, err := parseUtilizationRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "item_id and warehouse_id are required and must be valid UUIDs")
		return
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = reports.GranularityDay
	}
	var recipeIDs []uuid.UUID
	for _, s := range r.URL.Query()["recipe_id"] {
		if id, perr := uuid.Parse(s); perr == nil {
			recipeIDs = append(recipeIDs, id)
		}
	}
	result, err := h.reportsSvc.GetTimeseries(r.Context(), tenantID, itemID, warehouseID, from, to, granularity, recipeIDs)
	if err != nil {
		h.log.Error("ingredient utilization timeseries failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute ingredient utilization timeseries")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// IngredientUtilizationByRecipe handles GET /inventory/reports/ingredient-utilization/by-recipe
//
//	@Summary  Which recipes consumed this ingredient, and how much
//	@Tags     Reports
//	@Param    item_id       query  string  true   "Ingredient item ID"
//	@Param    warehouse_id  query  string  true   "Warehouse ID"
//	@Param    from          query  string  false  "Period start (YYYY-MM-DD), default 30 days ago"
//	@Param    to            query  string  false  "Period end (YYYY-MM-DD), default now"
//	@Success  200  {array}  reports.RecipeBreakdownRow
//	@Router   /{tenant}/inventory/reports/ingredient-utilization/by-recipe [get]
func (h *InventoryExtrasHandler) IngredientUtilizationByRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.reportsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Reports service not initialized")
		return
	}
	itemID, warehouseID, from, to, err := parseUtilizationRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "item_id and warehouse_id are required and must be valid UUIDs")
		return
	}
	result, err := h.reportsSvc.GetByRecipe(r.Context(), tenantID, itemID, warehouseID, from, to)
	if err != nil {
		h.log.Error("ingredient utilization by-recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute ingredient utilization by-recipe breakdown")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
