package handlers

import (
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/recipes"
)

// SetVarianceService injects the food cost variance service.
func (h *InventoryExtrasHandler) SetVarianceService(svc *recipes.VarianceService) {
	h.varianceSvc = svc
}

// SetMenuEngineeringService injects the menu engineering service.
func (h *InventoryExtrasHandler) SetMenuEngineeringService(svc *recipes.MenuEngineeringService) {
	h.menuEngSvc = svc
}

// FoodCostVarianceReport handles GET /inventory/reports/food-cost-variance
func (h *InventoryExtrasHandler) FoodCostVarianceReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.varianceSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Variance service not initialized")
		return
	}

	now := time.Now().UTC()
	from, to := now.AddDate(0, -1, 0), now

	if s := r.URL.Query().Get("from"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			from = t
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			from = t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			to = t
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			to = t
		}
	}

	tenantSlug := r.Header.Get("X-Tenant-Slug")
	if tenantSlug == "" {
		tenantSlug = r.URL.Query().Get("tenant_slug")
	}

	var result interface{}
	if r.URL.Query().Get("recalculate") == "true" && tenantSlug != "" {
		result, err = h.varianceSvc.CalculatePeriodVariance(r.Context(), tenantID, tenantSlug, from, to)
	} else {
		result, err = h.varianceSvc.GetStoredVariance(r.Context(), tenantID, from, to)
	}
	if err != nil {
		h.log.Error("food cost variance report failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate variance report")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// MenuEngineeringReport handles GET /inventory/reports/menu-engineering
func (h *InventoryExtrasHandler) MenuEngineeringReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.menuEngSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Menu engineering service not initialized")
		return
	}

	now := time.Now().UTC()
	from, to := now.AddDate(0, -1, 0), now

	if s := r.URL.Query().Get("from"); s != "" {
		if t, parseErr := time.Parse("2006-01-02", s); parseErr == nil {
			from = t
		} else if t, parseErr := time.Parse(time.RFC3339, s); parseErr == nil {
			from = t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, parseErr := time.Parse("2006-01-02", s); parseErr == nil {
			to = t
		} else if t, parseErr := time.Parse(time.RFC3339, s); parseErr == nil {
			to = t
		}
	}

	tenantSlug := r.Header.Get("X-Tenant-Slug")
	if tenantSlug == "" {
		tenantSlug = r.URL.Query().Get("tenant_slug")
	}

	result, err := h.menuEngSvc.GetMatrix(r.Context(), tenantID, tenantSlug, from, to)
	if err != nil {
		h.log.Error("menu engineering report failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate menu engineering report")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
