package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/docs"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

// exportPageSize is the internal page size used to walk the FULL result set for an export —
// deliberately NOT going through pagination.Parse, whose MaxLimit=100 clamp exists to bound
// interactive list requests and would silently truncate an export to the first page.
const exportPageSize = 500

// exportMaxRows caps a single export as a sane safety limit (very large tenants only).
const exportMaxRows = 20000

// ProductsExportPDF renders the current product catalog (subject to the same filters as the
// catalog list) as a branded PDF/CSV document — reuses items.ListItems for the data (identical
// filtering/outlet-scoping the JSON catalog list uses) and the shared docs report engine for
// rendering (see report_pdf.go).
//
//	@Summary   Products export document (PDF/CSV)
//	@Tags      Reports
//	@Produce   application/pdf
//	@Param     format       query  string  false  "pdf | csv"
//	@Param     search       query  string  false  "name/SKU search"
//	@Param     category_id  query  string  false  "filter by category"
//	@Param     type         query  string  false  "GOODS | SERVICE | RECIPE | INGREDIENT | EQUIPMENT | VOUCHER"
//	@Param     status       query  string  false  "active | inactive | all (default active)"
//	@Param     use_case     query  string  false  "e.g. RETAIL, FOOD_BEVERAGE"
//	@Param     outlet_id    query  string  false  "override the ambient X-Outlet-ID scope"
//	@Param     group_by     query  string  false  "category — render one section per category instead of one flat table"
//	@Success   200  {file}  binary
//	@Security  bearerAuth
//	@Router    /{tenant}/inventory/items/export [get]
func (h *InventoryHandler) ProductsExportPDF(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()
	typeFilter := q.Get("type")
	statusFilter := q.Get("status")
	if statusFilter == "" {
		statusFilter = "active"
	}
	searchFilter := q.Get("search")
	useCaseFilter := q.Get("use_case")

	var categoryID, unitID, outletID *uuid.UUID
	if v, e := uuid.Parse(q.Get("category_id")); e == nil {
		categoryID = &v
	}
	if v, e := uuid.Parse(q.Get("unit_id")); e == nil {
		unitID = &v
	}
	// Explicit ?outlet_id= override (platform/back-office export for a specific branch)
	// falls back to the ambient X-Outlet-ID drill-down the catalog list itself uses.
	if v, e := uuid.Parse(q.Get("outlet_id")); e == nil {
		outletID = &v
	} else if oid := operatingOutletID(r); oid != uuid.Nil {
		outletID = &oid
	}
	var tagsFilter []string
	if tagsParam := q.Get("tags"); tagsParam != "" {
		for _, t := range strings.Split(tagsParam, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tagsFilter = append(tagsFilter, t)
			}
		}
	}

	ctx := r.Context()
	rows, total, err := fetchAllItems(ctx, h.itemsSvc, tenantID, typeFilter, statusFilter, categoryID, unitID, searchFilter, outletID, useCaseFilter, tagsFilter)
	if err != nil {
		h.log.Error("products export: list items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list products")
		return
	}

	groupByCat := q.Get("group_by") == "category"
	activeCount, categorySet := 0, map[uuid.UUID]struct{}{}
	for _, it := range rows {
		if it.IsActive {
			activeCount++
		}
		if it.CategoryID != nil {
			categorySet[*it.CategoryID] = struct{}{}
		}
	}

	rep := &docs.Report{
		Title:       "Products Export",
		Subtitle:    "Catalog Listing",
		Currency:    currencyOr(""),
		GeneratedAt: time.Now().UTC(),
		Landscape:   true,
		Cards: []docs.Card{
			{Label: "Products", Value: strconv.Itoa(total)},
			{Label: "Active", Value: strconv.Itoa(activeCount)},
			{Label: "Categories", Value: strconv.Itoa(len(categorySet))},
		},
	}

	if groupByCat {
		order, groups := groupByCategory(rows, func(it items.ItemDTO) string { return it.CategoryName })
		for _, name := range order {
			group := groups[name]
			rep.Sections = append(rep.Sections, docs.Section{
				Kind:    docs.SectionTable,
				Title:   name,
				Note:    fmt.Sprintf("%d product(s)", len(group)),
				Columns: productColumns(false),
				Rows:    productRows(group, false),
			})
		}
	} else {
		rep.Sections = append(rep.Sections, docs.Section{
			Kind:    docs.SectionTable,
			Title:   "Products",
			Note:    exportFilterNote(q),
			Columns: productColumns(true),
			Rows:    productRows(rows, true),
		})
	}
	applyBranding(rep, h.docSvc.GetBranding(ctx, tenantID))

	streamReportDoc(w, h.log, rep, format, "products")
}

// productColumns builds the export table header, optionally including the Category column
// (omitted when rows are already grouped into per-category sections).
func productColumns(withCategory bool) []docs.Column {
	cols := []docs.Column{
		{Header: "SKU", Weight: 1.3},
		{Header: "Name", Weight: 2.4},
	}
	if withCategory {
		cols = append(cols, docs.Column{Header: "Category", Weight: 1.5})
	}
	return append(cols,
		docs.Column{Header: "Type", Weight: 1},
		docs.Column{Header: "On Hand", Weight: 1, Align: "R"},
		docs.Column{Header: "Cost", Weight: 1.1, Money: true},
		docs.Column{Header: "Selling Price", Weight: 1.2, Money: true},
		docs.Column{Header: "Status", Weight: 1},
	)
}

// productRows renders one table row per item, matching productColumns' shape.
func productRows(rows []items.ItemDTO, withCategory bool) [][]docs.Cell {
	out := make([][]docs.Cell, 0, len(rows))
	for _, it := range rows {
		onHand := "—"
		if it.OnHand != nil {
			onHand = fmtQtyReport(*it.OnHand)
		}
		cost := "—"
		if it.CostPrice != nil {
			cost = formatMoney(*it.CostPrice)
		}
		price := "—"
		if it.SellingPrice != nil {
			price = formatMoney(*it.SellingPrice)
		}
		status := "Inactive"
		if it.IsActive {
			status = "Active"
		}
		row := []docs.Cell{docs.Text(it.SKU), docs.Text(it.Name)}
		if withCategory {
			row = append(row, docs.Text(ifBlank(it.CategoryName, "—")))
		}
		row = append(row, docs.Text(it.Type), docs.Text(onHand), docs.Text(cost), docs.Text(price), docs.Text(status))
		out = append(out, row)
	}
	return out
}

// fetchAllItems walks items.ListItems page-by-page (NOT via pagination.Parse, whose MaxLimit=100
// would silently truncate an export) up to exportMaxRows, returning the full filtered result set.
func fetchAllItems(
	ctx context.Context, svc ItemsServicer, tenantID uuid.UUID,
	typeFilter, statusFilter string, categoryID, unitID *uuid.UUID, search string, outletID *uuid.UUID,
	useCase string, tags []string,
) ([]items.ItemDTO, int, error) {
	var all []items.ItemDTO
	offset, total := 0, 0
	for {
		page, t, err := svc.ListItems(ctx, tenantID, typeFilter, statusFilter, exportPageSize, offset, categoryID, unitID, search, outletID, nil, useCase, tags...)
		if err != nil {
			return nil, 0, err
		}
		total = t
		all = append(all, page...)
		offset += len(page)
		if len(page) < exportPageSize || offset >= total || offset >= exportMaxRows {
			break
		}
	}
	return all, total, nil
}

// exportFilterNote renders the applied filters as a human-readable caption under the section
// heading, e.g. "Filtered by: category, type — search: \"soda\"" — so a downloaded document is
// self-describing about its own scope.
func exportFilterNote(q url.Values) string {
	var parts []string
	if v := q.Get("search"); v != "" {
		parts = append(parts, fmt.Sprintf("search: %q", v))
	}
	if v := q.Get("category_id"); v != "" {
		parts = append(parts, "category filtered")
	}
	if v := q.Get("type"); v != "" {
		parts = append(parts, "type: "+v)
	}
	if v := q.Get("use_case"); v != "" {
		parts = append(parts, "use case: "+v)
	}
	if v := q.Get("status"); v != "" && v != "active" {
		parts = append(parts, "status: "+v)
	}
	if v := q.Get("outlet_id"); v != "" {
		parts = append(parts, "outlet filtered")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Filters — " + strings.Join(parts, ", ")
}
