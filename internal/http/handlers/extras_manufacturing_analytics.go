package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/inventory-service/internal/ent"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
)

// ─── Manufacturing analytics dashboard (migrated from ERP manufacturing.analytics) ──

func (h *InventoryExtrasHandler) registerManufacturingAnalyticsRoutes(r chi.Router) {
	r.Get("/inventory/manufacturing/dashboard", h.ManufacturingDashboard)
}

// ManufacturingDashboard returns headline production KPIs for the tenant.
func (h *InventoryExtrasHandler) ManufacturingDashboard(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()
	base := func() *ent.ProductionBatchQuery {
		return h.orm.ProductionBatch.Query().Where(entpb.TenantID(tenantID))
	}
	statusCount := func(s entpb.Status) int {
		n, _ := base().Where(entpb.StatusEQ(s)).Count(ctx)
		return n
	}
	total, _ := base().Count(ctx)

	var qtyAgg []struct {
		Sum float64 `json:"sum"`
	}
	_ = base().Where(entpb.StatusEQ(entpb.StatusCompleted)).Aggregate(ent.Sum(entpb.FieldActualQuantity)).Scan(ctx, &qtyAgg)
	var producedQty float64
	if len(qtyAgg) > 0 {
		producedQty = qtyAgg[0].Sum
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_batches":           total,
		"total_produced_quantity": producedQty,
		"batches_by_status": map[string]int{
			"planned":     statusCount(entpb.StatusPlanned),
			"in_progress": statusCount(entpb.StatusInProgress),
			"completed":   statusCount(entpb.StatusCompleted),
			"cancelled":   statusCount(entpb.StatusCancelled),
			"failed":      statusCount(entpb.StatusFailed),
		},
	})
}
