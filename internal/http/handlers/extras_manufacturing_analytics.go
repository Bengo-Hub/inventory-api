package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entib "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entma "github.com/bengobox/inventory-service/internal/ent/manufacturinganalytics"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
)

// ─── Manufacturing analytics dashboard (migrated from ERP manufacturing.analytics) ──

func (h *InventoryExtrasHandler) registerManufacturingAnalyticsRoutes(r chi.Router) {
	r.Get("/inventory/manufacturing/dashboard", h.ManufacturingDashboard)
	r.Get("/inventory/manufacturing/analytics", h.GetManufacturingAnalytics)
	r.Post("/inventory/manufacturing/analytics/recompute", h.RecomputeManufacturingAnalytics)
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

	// Low raw-material alerts: INGREDIENT items at/under their reorder level.
	lowMaterials := make([]map[string]any, 0)
	bals, _ := h.orm.InventoryBalance.Query().Where(entib.TenantID(tenantID)).WithItem().All(ctx)
	for _, bal := range bals {
		it := bal.Edges.Item
		if it == nil || string(it.Type) != "INGREDIENT" {
			continue
		}
		if bal.ReorderLevel > 0 && bal.Available <= float64(bal.ReorderLevel) {
			lowMaterials = append(lowMaterials, map[string]any{
				"item_id": bal.ItemID, "sku": it.Sku, "name": it.Name,
				"available": bal.Available, "reorder_level": bal.ReorderLevel, "warehouse_id": bal.WarehouseID,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_batches":           total,
		"total_produced_quantity": producedQty,
		"completion_rate":         completionRate,
		"scrap_total":             scrapTotal,
		"recent_batches":          recentOut,
		"low_material_alerts":     lowMaterials,
		"batches_by_status": map[string]int{
			"planned":     statusCount(entpb.StatusPlanned),
			"in_progress": statusCount(entpb.StatusInProgress),
			"completed":   completed,
			"cancelled":   statusCount(entpb.StatusCancelled),
			"failed":      statusCount(entpb.StatusFailed),
		},
	})
}

// recomputeManufacturingAnalytics rebuilds the per-day ManufacturingAnalytics
// snapshots for a tenant from its production batches (bucketed by created_at day).
func (h *InventoryExtrasHandler) recomputeManufacturingAnalytics(ctx context.Context, tenantID uuid.UUID) (int, error) {
	batches, err := h.orm.ProductionBatch.Query().Where(entpb.TenantID(tenantID)).All(ctx)
	if err != nil {
		return 0, err
	}
	type agg struct {
		total, completed, failed                int
		producedQty, matCost, labor, overhead   float64
	}
	byDay := map[string]*agg{}
	for _, b := range batches {
		day := b.CreatedAt.UTC().Format("2006-01-02")
		a := byDay[day]
		if a == nil {
			a = &agg{}
			byDay[day] = a
		}
		a.total++
		a.labor += b.LaborCost
		a.overhead += b.OverheadCost
		switch b.Status {
		case entpb.StatusCompleted:
			a.completed++
			if b.ActualQuantity != nil {
				a.producedQty += *b.ActualQuantity
			}
			if b.UnitCost != nil && b.ActualQuantity != nil {
				a.matCost += (*b.UnitCost * *b.ActualQuantity) - b.LaborCost - b.OverheadCost
			}
		case entpb.StatusFailed:
			a.failed++
		}
	}
	for day, a := range byDay {
		existing, _ := h.orm.ManufacturingAnalytics.Query().
			Where(entma.TenantID(tenantID), entma.Date(day)).Only(ctx)
		if existing == nil {
			_, _ = h.orm.ManufacturingAnalytics.Create().
				SetTenantID(tenantID).SetDate(day).
				SetTotalBatches(a.total).SetCompletedBatches(a.completed).SetFailedBatches(a.failed).
				SetTotalProductionQty(a.producedQty).SetTotalRawMaterialCost(a.matCost).
				SetTotalLaborCost(a.labor).SetTotalOverheadCost(a.overhead).Save(ctx)
		} else {
			_, _ = h.orm.ManufacturingAnalytics.UpdateOneID(existing.ID).
				SetTotalBatches(a.total).SetCompletedBatches(a.completed).SetFailedBatches(a.failed).
				SetTotalProductionQty(a.producedQty).SetTotalRawMaterialCost(a.matCost).
				SetTotalLaborCost(a.labor).SetTotalOverheadCost(a.overhead).Save(ctx)
		}
	}
	return len(byDay), nil
}

// GetManufacturingAnalytics handles GET /inventory/manufacturing/analytics?start&end —
// historical daily production snapshots (recompute first if empty).
//
//	@Summary      Historical manufacturing analytics (daily)
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        start  query  string  false  "Start date YYYY-MM-DD"
//	@Param        end    query  string  false  "End date YYYY-MM-DD"
//	@Success      200    {array}  map[string]interface{}
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/manufacturing/analytics [get]
func (h *InventoryExtrasHandler) GetManufacturingAnalytics(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.ManufacturingAnalytics.Query().Where(entma.TenantID(tenantID))
	if s := r.URL.Query().Get("start"); s != "" {
		q = q.Where(entma.DateGTE(s))
	}
	if e := r.URL.Query().Get("end"); e != "" {
		q = q.Where(entma.DateLTE(e))
	}
	rows, _ := q.Order(ent.Asc(entma.FieldDate)).All(r.Context())
	out := make([]map[string]any, len(rows))
	for i, m := range rows {
		out[i] = map[string]any{
			"date": m.Date, "total_batches": m.TotalBatches, "completed_batches": m.CompletedBatches,
			"failed_batches": m.FailedBatches, "total_production_qty": m.TotalProductionQty,
			"total_raw_material_cost": m.TotalRawMaterialCost, "total_labor_cost": m.TotalLaborCost,
			"total_overhead_cost": m.TotalOverheadCost,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// RecomputeManufacturingAnalytics handles POST /inventory/manufacturing/analytics/recompute.
//
//	@Summary      Rebuild daily manufacturing analytics snapshots
//	@Tags         Manufacturing
//	@Produce      json
//	@Success      200  {object}  map[string]interface{}
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/manufacturing/analytics/recompute [post]
func (h *InventoryExtrasHandler) RecomputeManufacturingAnalytics(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	days, err := h.recomputeManufacturingAnalytics(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RECOMPUTE_FAILED", "Failed to recompute analytics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}
