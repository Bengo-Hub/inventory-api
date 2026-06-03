package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/inventory-service/internal/ent"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
)

// ─── Procurement analytics dashboard (migrated from ERP procurement.analytics) ──

func (h *InventoryExtrasHandler) registerProcurementAnalyticsRoutes(r chi.Router) {
	r.Get("/inventory/procurement/dashboard", h.ProcurementDashboard)
}

// ProcurementDashboard returns headline procurement KPIs for the tenant.
func (h *InventoryExtrasHandler) ProcurementDashboard(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()

	poBase := func() *ent.PurchaseOrderQuery {
		return h.orm.PurchaseOrder.Query().Where(entpo.TenantID(tenantID))
	}
	statusCount := func(s entpo.Status) int {
		n, _ := poBase().Where(entpo.StatusEQ(s)).Count(ctx)
		return n
	}

	totalPOs, _ := poBase().Count(ctx)
	var spendAgg []struct {
		Sum float64 `json:"sum"`
	}
	_ = poBase().Aggregate(ent.Sum(entpo.FieldTotalAmount)).Scan(ctx, &spendAgg)
	var totalSpend float64
	if len(spendAgg) > 0 {
		totalSpend = spendAgg[0].Sum
	}

	openRequisitions, _ := h.orm.Requisition.Query().
		Where(entreq.TenantID(tenantID), entreq.StatusIn(
			entreq.StatusDraft, entreq.StatusSubmitted, entreq.StatusProcurementReview, entreq.StatusApproved,
		)).Count(ctx)
	supplierCount, _ := h.orm.Supplier.Query().Where(entsupplier.TenantID(tenantID), entsupplier.IsActive(true)).Count(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"total_purchase_orders": totalPOs,
		"total_spend":           totalSpend,
		"open_requisitions":     openRequisitions,
		"active_suppliers":      supplierCount,
		"purchase_orders_by_status": map[string]int{
			"draft":              statusCount(entpo.StatusDraft),
			"sent":               statusCount(entpo.StatusSent),
			"partially_received": statusCount(entpo.StatusPartiallyReceived),
			"received":           statusCount(entpo.StatusReceived),
			"cancelled":          statusCount(entpo.StatusCancelled),
		},
	})
}
