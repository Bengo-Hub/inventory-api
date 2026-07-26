package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/docs"
)

// StockExportPDF renders the current stock-levels list (subject to the same filters as the
// stock page, plus warehouse/location drill-down) as a branded PDF/CSV document. Reuses
// queryStockLevels — the SAME query the JSON /inventory/stock endpoint runs — for the data, and
// the shared docs report engine (see report_pdf.go) for rendering.
//
//	@Summary   Stock levels export document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format        query  string  false  "pdf | csv"
//	@Param     search        query  string  false  "item/SKU/warehouse search"
//	@Param     category_id   query  string  false  "filter by category"
//	@Param     type          query  string  false  "GOODS | INGREDIENT | EQUIPMENT"
//	@Param     warehouse_id  query  string  false  "filter to one warehouse"
//	@Param     location_id   query  string  false  "filter to one warehouse sub-location (zone/aisle/rack/shelf/bin)"
//	@Param     outlet_id     query  string  false  "override the ambient X-Outlet-ID scope"
//	@Param     low_stock     query  bool    false  "only items at/below reorder level"
//	@Param     out_of_stock  query  bool    false  "only items with zero available"
//	@Param     group_by      query  string  false  "category — render one section per category instead of one flat table"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/stock/export [get]
func (h *InventoryExtrasHandler) StockExportPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
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
	rows, err := h.queryStockLevels(ctx, tenantID, parseStockLevelFilters(r, "outlet_id"))
	if err != nil {
		h.log.Error("stock export: query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list stock")
		return
	}

	warehouseSet := map[string]struct{}{}
	lowCount, outCount := 0, 0
	for _, s := range rows {
		warehouseSet[s.WarehouseID.String()] = struct{}{}
		isOut := s.Available <= 0
		isLow := s.ReorderPoint != nil && s.Available > 0 && s.Available <= float64(*s.ReorderPoint)
		if isOut {
			outCount++
		} else if isLow {
			lowCount++
		}
	}

	rep := &docs.Report{
		Title:       "Stock Levels Export",
		Subtitle:    "On-Hand Availability by Warehouse",
		GeneratedAt: time.Now().UTC(),
		Landscape:   true,
		Cards: []docs.Card{
			{Label: "Stock Rows", Value: strconv.Itoa(len(rows))},
			{Label: "Warehouses", Value: strconv.Itoa(len(warehouseSet))},
			{Label: "Low Stock", Value: strconv.Itoa(lowCount)},
			{Label: "Out of Stock", Value: strconv.Itoa(outCount)},
		},
	}

	if r.URL.Query().Get("group_by") == "category" {
		order, groups := groupByCategory(rows, func(s stockLevelDTO) string { return s.CategoryName })
		for _, name := range order {
			group := groups[name]
			rep.Sections = append(rep.Sections, docs.Section{
				Kind:    docs.SectionTable,
				Title:   name,
				Note:    fmt.Sprintf("%d row(s)", len(group)),
				Columns: stockColumns(false),
				Rows:    stockRows(group, false),
			})
		}
	} else {
		rep.Sections = append(rep.Sections, docs.Section{
			Kind:    docs.SectionTable,
			Title:   "Stock Levels",
			Columns: stockColumns(true),
			Rows:    stockRows(rows, true),
		})
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	streamReportDoc(w, h.log, rep, format, "stock-levels")
}

// stockColumns builds the export table header, optionally including the Category column
// (omitted when rows are already grouped into per-category sections).
func stockColumns(withCategory bool) []docs.Column {
	cols := []docs.Column{{Header: "Item / SKU", Weight: 2.4}}
	if withCategory {
		cols = append(cols, docs.Column{Header: "Category", Weight: 1.4})
	}
	return append(cols,
		docs.Column{Header: "Warehouse", Weight: 1.6},
		docs.Column{Header: "Location", Weight: 1.3},
		docs.Column{Header: "Available", Weight: 1.2, Align: "R"},
		docs.Column{Header: "Reserved", Weight: 1, Align: "R"},
		docs.Column{Header: "Reorder Pt", Weight: 1, Align: "R"},
		docs.Column{Header: "Status", Weight: 1.1},
	)
}

// stockRows renders one table row per stock balance, matching stockColumns' shape.
func stockRows(rows []stockLevelDTO, withCategory bool) [][]docs.Cell {
	out := make([][]docs.Cell, 0, len(rows))
	for _, s := range rows {
		isOut := s.Available <= 0
		isLow := s.ReorderPoint != nil && s.Available > 0 && s.Available <= float64(*s.ReorderPoint)
		reorder := "—"
		if s.ReorderPoint != nil {
			reorder = strconv.Itoa(*s.ReorderPoint)
		}
		status := "In Stock"
		switch {
		case isOut:
			status = "Out of Stock"
		case isLow:
			status = "Low Stock"
		}
		row := []docs.Cell{docs.Text(itemLabel(s.ItemName, s.SKU))}
		if withCategory {
			row = append(row, docs.Text(ifBlank(s.CategoryName, "—")))
		}
		row = append(row,
			docs.Text(s.WarehouseName),
			docs.Text(ifBlank(s.LocationName, "—")),
			docs.Text(fmtQtyReport(s.Available)+" "+s.Unit),
			docs.Text(fmtQtyReport(s.Reserved)),
			docs.Text(reorder),
			docs.Text(status),
		)
		out = append(out, row)
	}
	return out
}
