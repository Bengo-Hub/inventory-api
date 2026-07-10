package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/docs"
)

// IngredientUtilizationReportPDF renders the ingredient utilization report (summary + recipe
// breakdown) as a branded PDF/CSV. Mirrors report_pdf.go's pattern — reuses reportsSvc's exact
// JSON computation, only re-shapes it into a docs.Report — kept in its own file since
// report_pdf.go was already at the file-length guideline (see feedback_file_length_modular).
//
//	@Summary   Ingredient utilization report document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format        query  string  false  "pdf | csv"
//	@Param     item_id       query  string  true   "Ingredient item ID"
//	@Param     warehouse_id  query  string  true   "Warehouse ID"
//	@Param     from          query  string  false  "period start (YYYY-MM-DD)"
//	@Param     to            query  string  false  "period end (YYYY-MM-DD)"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/reports/ingredient-utilization.pdf [get]
func (h *InventoryExtrasHandler) IngredientUtilizationReportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.reportsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Reports service not initialized")
		return
	}
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	format, err := docs.FormatFromString(r.URL.Query().Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORMAT", err.Error())
		return
	}
	itemID, warehouseID, from, to, err := parseUtilizationRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "item_id and warehouse_id are required and must be valid UUIDs")
		return
	}

	ctx := r.Context()
	summary, err := h.reportsSvc.GetSummary(ctx, tenantID, itemID, warehouseID, from, to)
	if err != nil {
		h.log.Error("ingredient utilization report pdf: summary failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate ingredient utilization report")
		return
	}
	breakdown, err := h.reportsSvc.GetByRecipe(ctx, tenantID, itemID, warehouseID, from, to)
	if err != nil {
		h.log.Error("ingredient utilization report pdf: by-recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate ingredient utilization report")
		return
	}

	rows := make([][]docs.Cell, 0, len(breakdown))
	bars := make([]docs.Bar, 0, min(len(breakdown), 8))
	for i, row := range breakdown {
		name := row.RecipeName
		if name == "" {
			name = "Direct sale"
		}
		rows = append(rows, []docs.Cell{
			docs.Text(itemLabel(name, row.RecipeSKU)),
			docs.Text(fmtQtyReport(row.Quantity) + " " + summary.Unit),
			docs.Text(formatMoney(row.Cost)),
			docs.Text(fmt.Sprintf("%.1f%%", row.PctOfTotal)),
		})
		if i < 8 {
			bars = append(bars, docs.Bar{Label: name, Value: row.Quantity})
		}
	}

	cover := "—"
	if summary.ProjectedDaysOfCover != nil {
		cover = fmt.Sprintf("%.1f days", *summary.ProjectedDaysOfCover)
	}

	rep := &docs.Report{
		Title:       "Ingredient Utilization Report",
		Subtitle:    fmt.Sprintf("%s (%s)", summary.ItemName, summary.ItemSKU),
		PeriodFrom:  from,
		PeriodTo:    to,
		GeneratedAt: time.Now().UTC(),
		Cards: []docs.Card{
			{Label: "Purchased", Value: fmtQtyReport(summary.PurchasedQty) + " " + summary.Unit},
			{Label: "Consumed", Value: fmtQtyReport(summary.ConsumedQty) + " " + summary.Unit},
			{Label: "On Hand", Value: fmtQtyReport(summary.OnHand) + " " + summary.Unit},
			{Label: "Reorder Level", Value: strconv.Itoa(summary.ReorderLevel)},
			{Label: "Days of Cover", Value: cover},
		},
		Sections: []docs.Section{{
			Kind:  docs.SectionTable,
			Title: "Consumption by Recipe",
			Columns: []docs.Column{
				{Header: "Recipe / SKU", Weight: 3},
				{Header: "Quantity", Weight: 1.3, Align: "R"},
				{Header: "Cost", Weight: 1.2, Money: true},
				{Header: "Share", Weight: 1, Align: "R"},
			},
			Rows: rows,
			Total: []docs.Cell{
				docs.BoldText("Total"), docs.Text(""), docs.BoldText(formatMoney(summary.ConsumedCost)), docs.Text(""),
			},
		}, {
			Kind:  docs.SectionChart,
			Title: "Top Recipes by Quantity Consumed",
			Bars:  bars,
		}},
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	streamReportDoc(w, h.log, rep, format, "ingredient-utilization")
}
