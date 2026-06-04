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
//
//	@Summary      Manufacturing KPI dashboard
//	@Tags         Manufacturing
//	@Produce      json
//	@Success      200  {object}  map[string]interface{}
//	@Failure      400  {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/manufacturing/dashboard [get]
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

	completed := statusCount(entpb.StatusCompleted)
	completionRate := 0.0
	if total > 0 {
		completionRate = float64(completed) / float64(total)
	}

	var scrapAgg []struct {
		Sum float64 `json:"sum"`
	}
	_ = base().Aggregate(ent.Sum(entpb.FieldScrapQuantity)).Scan(ctx, &scrapAgg)
	var scrapTotal float64
	if len(scrapAgg) > 0 {
		scrapTotal = scrapAgg[0].Sum
	}

	recent, _ := base().Order(ent.Desc(entpb.FieldCreatedAt)).Limit(5).All(ctx)
	recentOut := make([]map[string]any, 0, len(recent))
	for _, b := range recent {
		recentOut = append(recentOut, map[string]any{
			"id": b.ID, "batch_number": b.BatchNumber, "status": b.Status,
			"planned_quantity": b.PlannedQuantity, "actual_quantity": b.ActualQuantity,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_batches":           total,
		"total_produced_quantity": producedQty,
		"completion_rate":         completionRate,
		"scrap_total":             scrapTotal,
		"recent_batches":          recentOut,
		"batches_by_status": map[string]int{
			"planned":     statusCount(entpb.StatusPlanned),
			"in_progress": statusCount(entpb.StatusInProgress),
			"completed":   completed,
			"cancelled":   statusCount(entpb.StatusCancelled),
			"failed":      statusCount(entpb.StatusFailed),
		},
	})
}
