package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/docs"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
)

// This file renders the existing JSON inventory reports (stock valuation, deadstock, food-cost
// variance, menu engineering) as premium, tenant-branded PDF/CSV documents via the docs engine.
// Each handler reuses the SAME service computation as its JSON sibling — it only re-shapes the
// already-computed data into a docs.Report and streams it. Branding is resolved exactly like the
// Purchase Order PDF (documents.Service.GetBranding + logo fetch).

// currencyOr returns c, or a sensible default when the report carries no currency.
func currencyOr(c string) string {
	if strings.TrimSpace(c) == "" {
		return "KES"
	}
	return c
}

// fmtQtyReport renders a quantity trimming trailing zeros (e.g. "12", "2.5").
func fmtQtyReport(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// applyBranding copies the resolved tenant branding onto a report (identity + logo + primary color),
// fetching the logo bytes the same way RenderPurchaseOrderPDF does.
func applyBranding(rep *docs.Report, b documents.Branding) {
	rep.TenantName = b.CompanyName
	rep.Address = strings.Join(b.Address, "\n")
	rep.PrimaryColor = b.PrimaryColor
	rep.ProviderFooterEnabled = b.ProviderFooterEnabled
	if logo, logoType := documents.FetchLogoBytes(b.LogoURL); len(logo) > 0 {
		rep.LogoPNG = logo
		rep.LogoType = logoType
	}
	// GetBranding already fetches Phone/Email/KRAPIN (used by the invoice/PO document engine), but
	// docs.Report has no dedicated fields for them — without this they'd be silently dropped even
	// though the data is right there. The meta box already renders arbitrary [][2]string rows, so
	// surface them there instead of leaving reports with less contact identity than an invoice.
	if b.Phone != "" {
		rep.Meta = append(rep.Meta, [2]string{"Phone", b.Phone})
	}
	if b.Email != "" {
		rep.Meta = append(rep.Meta, [2]string{"Email", b.Email})
	}
	if b.KRAPIN != "" {
		rep.Meta = append(rep.Meta, [2]string{"KRA PIN", b.KRAPIN})
	}
}

// streamReportDoc generates the report in the requested format and streams it inline. The filename is
// "<baseName>-<YYYY-MM-DD>.<ext>". Mirrors the streaming pattern used by GeneratePurchaseOrderPDF.
func streamReportDoc(w http.ResponseWriter, log *zap.Logger, rep *docs.Report, format docs.Format, baseName string) {
	svc := &docs.DocumentService{}
	body, mime, err := svc.Generate(rep, format)
	if err != nil {
		if log != nil {
			log.Error("report document render failed", zap.String("report", baseName), zap.Error(err))
		}
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render report document")
		return
	}
	ext := "pdf"
	if format == docs.FormatCSV {
		ext = "csv"
	}
	filename := fmt.Sprintf("%s-%s.%s", baseName, time.Now().Format("2006-01-02"), ext)
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// reportPeriodParams parses the ?from=&to= date range used by the period-scoped reports (variance,
// menu engineering), defaulting to the last month. Accepts "2006-01-02" or RFC3339.
func reportPeriodParams(r *http.Request) (from, to time.Time) {
	now := time.Now().UTC()
	from, to = now.AddDate(0, -1, 0), now
	parse := func(s string) (time.Time, bool) {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	if s := r.URL.Query().Get("from"); s != "" {
		if t, ok := parse(s); ok {
			from = t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, ok := parse(s); ok {
			to = t
		}
	}
	return from, to
}

// reportTenantSlug resolves the tenant slug used for S2S sales lookups (variance/menu), from the
// X-Tenant-Slug header or ?tenant_slug=.
func reportTenantSlug(r *http.Request) string {
	slug := r.Header.Get("X-Tenant-Slug")
	if slug == "" {
		slug = r.URL.Query().Get("tenant_slug")
	}
	return slug
}

// StockValuationReportPDF renders the stock valuation report as a branded PDF/CSV.
//
//	@Summary   Stock valuation report document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format  query  string  false  "pdf | csv"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/reports/stock-valuation.pdf [get]
func (h *InventoryHandler) StockValuationReportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	format, err := docs.FormatFromString(r.URL.Query().Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORMAT", err.Error())
		return
	}
	ctx := r.Context()
	val, err := h.itemsSvc.StockValuation(ctx, tenantID)
	if err != nil {
		h.log.Error("stock valuation report pdf failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute stock valuation")
		return
	}
	cur := currencyOr(val.Currency)

	rep := &docs.Report{
		Title:       "Stock Valuation Report",
		Subtitle:    "Inventory On-Hand Valuation",
		Currency:    cur,
		GeneratedAt: time.Now().UTC(),
		Cards: []docs.Card{
			{Label: "Total Value", Value: cur + " " + formatMoney(val.TotalValue)},
			{Label: "SKU Count", Value: strconv.Itoa(val.ItemCount), Sub: fmtQtyReport(val.TotalUnits) + " units on hand"},
		},
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	// Top items by value.
	rows := make([][]docs.Cell, 0, len(val.TopItems))
	for _, it := range val.TopItems {
		rows = append(rows, []docs.Cell{
			docs.Text(itemLabel(it.Name, it.SKU)),
			docs.Text(fmtQtyReport(it.OnHand)),
			docs.Text(formatMoney(it.UnitCost)),
			docs.Text(formatMoney(it.Value)),
		})
	}
	rep.Sections = append(rep.Sections, docs.Section{
		Kind:  docs.SectionTable,
		Title: "Top Items by Value",
		Columns: []docs.Column{
			{Header: "Item / SKU", Weight: 3},
			{Header: "Qty on Hand", Weight: 1.1, Align: "R"},
			{Header: "Unit Cost (" + cur + ")", Weight: 1.3, Money: true},
			{Header: "Value (" + cur + ")", Weight: 1.4, Money: true},
		},
		Rows: rows,
		Total: []docs.Cell{
			docs.BoldText("Total"), docs.Text(""), docs.Text(""), docs.BoldText(formatMoney(val.TotalValue)),
		},
	})

	// Value by category.
	if len(val.ByCategory) > 0 {
		catRows := make([][]docs.Cell, 0, len(val.ByCategory))
		for _, c := range val.ByCategory {
			catRows = append(catRows, []docs.Cell{
				docs.Text(ifBlank(c.CategoryName, "Uncategorized")),
				docs.Text(strconv.Itoa(c.ItemCount)),
				docs.Text(fmtQtyReport(c.TotalUnits)),
				docs.Text(formatMoney(c.TotalValue)),
			})
		}
		rep.Sections = append(rep.Sections, docs.Section{
			Kind:  docs.SectionTable,
			Title: "Value by Category",
			Columns: []docs.Column{
				{Header: "Category", Weight: 3},
				{Header: "Items", Weight: 1, Align: "R"},
				{Header: "Units", Weight: 1, Align: "R"},
				{Header: "Value (" + cur + ")", Weight: 1.4, Money: true},
			},
			Rows: catRows,
		})
	}

	streamReportDoc(w, h.log, rep, format, "stock-valuation")
}

// StockDeadstockReportPDF renders the deadstock report as a branded PDF/CSV.
//
//	@Summary   Deadstock report document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format  query  string  false  "pdf | csv"
//	@Param     days    query  int     false  "lookback window (default 90)"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/reports/deadstock.pdf [get]
func (h *InventoryHandler) StockDeadstockReportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	format, err := docs.FormatFromString(r.URL.Query().Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORMAT", err.Error())
		return
	}
	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		if n, e := strconv.Atoi(d); e == nil && n > 0 {
			days = n
		}
	}
	ctx := r.Context()
	dead, err := h.itemsSvc.StockDeadstock(ctx, tenantID, days)
	if err != nil {
		h.log.Error("deadstock report pdf failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute deadstock report")
		return
	}
	cur := currencyOr(dead.Currency)

	rep := &docs.Report{
		Title:       "Deadstock Report",
		Subtitle:    fmt.Sprintf("No outbound movement in %d days", days),
		Currency:    cur,
		GeneratedAt: time.Now().UTC(),
		Landscape:   true,
		Cards: []docs.Card{
			{Label: "Capital Tied Up", Value: cur + " " + formatMoney(dead.TotalDeadValue)},
			{Label: "Dead / Slow Items", Value: strconv.Itoa(dead.ItemCount)},
			{Label: "Lookback", Value: strconv.Itoa(days) + " days"},
		},
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	rows := make([][]docs.Cell, 0, len(dead.Items))
	for _, it := range dead.Items {
		last := "—"
		if !it.LastActivity.IsZero() {
			last = it.LastActivity.Format("02 Jan 2006")
		}
		rows = append(rows, []docs.Cell{
			docs.Text(itemLabel(it.Name, it.SKU)),
			docs.Text(ifBlank(it.CategoryName, "—")),
			docs.Text(fmtQtyReport(it.OnHand)),
			docs.Text(formatMoney(it.UnitCost)),
			docs.Text(formatMoney(it.Value)),
			docs.Text(last),
		})
	}
	rep.Sections = append(rep.Sections, docs.Section{
		Kind:  docs.SectionTable,
		Title: "Non-Moving Stock",
		Columns: []docs.Column{
			{Header: "Item / SKU", Weight: 3},
			{Header: "Category", Weight: 1.6},
			{Header: "Qty on Hand", Weight: 1.1, Align: "R"},
			{Header: "Unit Cost (" + cur + ")", Weight: 1.2, Money: true},
			{Header: "Value (" + cur + ")", Weight: 1.3, Money: true},
			{Header: "Last Activity", Weight: 1.3, Align: "R"},
		},
		Rows: rows,
		Total: []docs.Cell{
			docs.BoldText("Total"), docs.Text(""), docs.Text(""), docs.Text(""), docs.BoldText(formatMoney(dead.TotalDeadValue)), docs.Text(""),
		},
	})

	streamReportDoc(w, h.log, rep, format, "deadstock")
}

// FoodCostVarianceReportPDF renders the food-cost variance report as a branded PDF/CSV.
//
//	@Summary   Food-cost variance report document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format  query  string  false  "pdf | csv"
//	@Param     from    query  string  false  "period start (YYYY-MM-DD)"
//	@Param     to      query  string  false  "period end (YYYY-MM-DD)"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/reports/food-cost-variance.pdf [get]
func (h *InventoryExtrasHandler) FoodCostVarianceReportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.varianceSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Variance service not initialized")
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
	ctx := r.Context()
	from, to := reportPeriodParams(r)
	slug := reportTenantSlug(r)

	var report []recipes.VarianceReportDTO
	if r.URL.Query().Get("recalculate") == "true" && slug != "" {
		report, err = h.varianceSvc.CalculatePeriodVariance(ctx, tenantID, slug, from, to)
	} else {
		report, err = h.varianceSvc.GetStoredVariance(ctx, tenantID, from, to)
	}
	if err != nil {
		h.log.Error("food cost variance report pdf failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate variance report")
		return
	}

	var totalVar float64
	rows := make([][]docs.Cell, 0, len(report))
	for _, v := range report {
		totalVar += v.VariancePct
		rows = append(rows, []docs.Cell{
			docs.Text(itemLabel(v.RecipeName, v.RecipeSKU)),
			docs.Text(formatMoney(v.TheoreticalCost)),
			docs.Text(formatMoney(v.ActualCost)),
			docs.Text(fmt.Sprintf("%+.1f%%", v.VariancePct)),
		})
	}
	avgVar := 0.0
	if len(report) > 0 {
		avgVar = totalVar / float64(len(report))
	}

	rep := &docs.Report{
		Title:       "Food-Cost Variance Report",
		Subtitle:    "Theoretical vs Actual Recipe Cost",
		Currency:    currencyOr(""),
		PeriodFrom:  from,
		PeriodTo:    to,
		GeneratedAt: time.Now().UTC(),
		Cards: []docs.Card{
			{Label: "Recipes", Value: strconv.Itoa(len(report))},
			{Label: "Avg Variance", Value: fmt.Sprintf("%+.1f%%", avgVar)},
		},
		Sections: []docs.Section{{
			Kind:  docs.SectionTable,
			Title: "Recipe Cost Variance",
			Columns: []docs.Column{
				{Header: "Recipe / SKU", Weight: 3},
				{Header: "Theoretical", Weight: 1.4, Money: true},
				{Header: "Actual", Weight: 1.4, Money: true},
				{Header: "Variance %", Weight: 1.2, Align: "R"},
			},
			Rows: rows,
		}},
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	streamReportDoc(w, h.log, rep, format, "food-cost-variance")
}

// MenuEngineeringReportPDF renders the menu engineering matrix as a branded PDF/CSV.
//
//	@Summary   Menu engineering report document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format  query  string  false  "pdf | csv"
//	@Param     from    query  string  false  "period start (YYYY-MM-DD)"
//	@Param     to      query  string  false  "period end (YYYY-MM-DD)"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/reports/menu-engineering.pdf [get]
func (h *InventoryExtrasHandler) MenuEngineeringReportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.menuEngSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Menu engineering service not initialized")
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
	ctx := r.Context()
	from, to := reportPeriodParams(r)
	slug := reportTenantSlug(r)

	matrix, err := h.menuEngSvc.GetMatrix(ctx, tenantID, slug, from, to)
	if err != nil {
		h.log.Error("menu engineering report pdf failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to generate menu engineering report")
		return
	}

	// Category counts for the summary cards + chart.
	counts := map[recipes.MenuCategory]int{}
	rows := make([][]docs.Cell, 0, len(matrix))
	for _, m := range matrix {
		counts[m.Category]++
		price := "—"
		if m.SuggestedPrice != nil {
			price = formatMoney(*m.SuggestedPrice)
		}
		rows = append(rows, []docs.Cell{
			docs.Text(itemLabel(m.RecipeName, m.RecipeSKU)),
			docs.Text(string(m.Category)),
			docs.Text(fmtQtyReport(m.Popularity)),
			docs.Text(fmt.Sprintf("%.1f%%", m.ContribMargin)),
			docs.Text(price),
		})
	}

	rep := &docs.Report{
		Title:       "Menu Engineering Report",
		Subtitle:    "Popularity vs Contribution Margin",
		Currency:    currencyOr(""),
		PeriodFrom:  from,
		PeriodTo:    to,
		GeneratedAt: time.Now().UTC(),
		Cards: []docs.Card{
			{Label: "Stars", Value: strconv.Itoa(counts[recipes.MenuCategoryStar])},
			{Label: "Plowhorses", Value: strconv.Itoa(counts[recipes.MenuCategoryPlowhorse])},
			{Label: "Puzzles", Value: strconv.Itoa(counts[recipes.MenuCategoryPuzzle])},
			{Label: "Dogs", Value: strconv.Itoa(counts[recipes.MenuCategoryDog])},
		},
		Sections: []docs.Section{{
			Kind:  docs.SectionTable,
			Title: "Menu Item Classification",
			Columns: []docs.Column{
				{Header: "Recipe / SKU", Weight: 3},
				{Header: "Class", Weight: 1.2},
				{Header: "Units Sold", Weight: 1.1, Align: "R"},
				{Header: "Margin %", Weight: 1.1, Align: "R"},
				{Header: "Suggested Price", Weight: 1.3, Money: true},
			},
			Rows: rows,
		}, {
			Kind:  docs.SectionChart,
			Title: "Menu Mix by Class",
			Bars: []docs.Bar{
				{Label: "Stars", Value: float64(counts[recipes.MenuCategoryStar])},
				{Label: "Plowhorses", Value: float64(counts[recipes.MenuCategoryPlowhorse])},
				{Label: "Puzzles", Value: float64(counts[recipes.MenuCategoryPuzzle])},
				{Label: "Dogs", Value: float64(counts[recipes.MenuCategoryDog])},
			},
		}},
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	streamReportDoc(w, h.log, rep, format, "menu-engineering")
}

// itemLabel builds the "Name  ·  SKU" cell used across report tables (falls back gracefully).
func itemLabel(name, sku string) string {
	name = strings.TrimSpace(name)
	sku = strings.TrimSpace(sku)
	switch {
	case name != "" && sku != "":
		return name + "  ·  " + sku
	case name != "":
		return name
	default:
		return sku
	}
}

// ifBlank returns fallback when s is blank.
func ifBlank(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
