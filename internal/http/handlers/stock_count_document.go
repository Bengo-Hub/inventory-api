package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	entcountline "github.com/bengobox/inventory-service/internal/ent/stockcountline"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GenerateStockCountPDF renders a stock-take document
// (GET /inventory/stock-counts/{id}/pdf?mode=blank|variance).
//
// One count renders as either of two documents:
//
//	blank    — the count sheet handed to staff: system quantity printed, COUNTED/VARIANCE left
//	           ruled and empty to fill in by hand.
//	variance — the post-count reconciliation report, fully populated.
//
// mode defaults to whichever the count's CURRENT status actually supports: a count that has been
// counted (review/approved/posted) defaults to the variance report, anything still open defaults
// to the blank sheet — mirroring how the transfer document endpoint picks its default by status.
//
//	@Summary      Generate a stock take count sheet or variance report PDF
//	@Tags         stock
//	@Produce      application/pdf
//	@Param        id    path      string  true   "Stock count ID"
//	@Param        mode  query     string  false  "blank | variance (defaults by count status)"
//	@Success      200   {file}    binary
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/stock-counts/{id}/pdf [get]
func (h *StockCountHandler) GenerateStockCountPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return
	}
	ctx := r.Context()
	count, err := h.orm.StockCount.Query().
		Where(entcount.ID(countID), entcount.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	switch mode {
	case documents.StockCountModeBlank, documents.StockCountModeVariance:
		// Explicit caller choice wins.
	case "":
		mode = defaultStockCountMode(string(count.Status))
	default:
		writeError(w, http.StatusBadRequest, "INVALID_MODE", `mode must be "blank" or "variance"`)
		return
	}

	lines, _ := h.orm.StockCountLine.Query().
		Where(entcountline.StockCountID(countID), entcountline.TenantID(tenantID)).All(ctx)

	// Item name + stock unit, so the sheet reads naturally instead of showing raw UUIDs.
	itemIDs := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	names, units := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	if len(itemIDs) > 0 {
		// WithUnits matches the JSON Get handler's enrichment — the unit abbreviation ("kg",
		// "pcs") is what makes a count sheet legible on the shop floor.
		if items, e := h.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).
			WithUnits().All(ctx); e == nil {
			for _, it := range items {
				names[it.ID] = it.Name
				if it.Edges.Units != nil {
					units[it.ID] = it.Edges.Units.Abbreviation
				}
			}
		}
	}

	items := make([]documents.StockCountDocLine, 0, len(lines))
	for _, l := range lines {
		line := documents.StockCountDocLine{
			Desc:      ifEmptyStr(names[l.ItemID], l.ItemID.String()),
			SubDesc:   l.Sku,
			Unit:      units[l.ItemID],
			SystemQty: formatQty(l.SystemQty),
			Reason:    l.Reason,
		}
		if l.CountedQty != nil {
			line.CountedQty = formatQty(*l.CountedQty)
		}
		if l.Variance != nil {
			line.Variance = signedQty(*l.Variance)
		}
		items = append(items, line)
	}
	// Stable, human order: by item name (falling back to SKU) so a printed sheet matches how
	// shelves are walked, not row insertion order — same ordering as the JSON Get handler.
	sort.Slice(items, func(i, j int) bool {
		ni, nj := items[i].Desc, items[j].Desc
		if ni == "" {
			ni = items[i].SubDesc
		}
		if nj == "" {
			nj = items[j].SubDesc
		}
		return strings.ToLower(ni) < strings.ToLower(nj)
	})

	warehouseName := ""
	if wh, e := h.orm.Warehouse.Query().
		Where(entwarehouse.ID(count.WarehouseID), entwarehouse.TenantID(tenantID)).Only(ctx); e == nil {
		warehouseName = wh.Name
	}

	doc := documents.StockCountDoc{
		Branding:      h.docSvc.GetBranding(ctx, tenantID),
		Mode:          mode,
		Reference:     count.Reference,
		Date:          count.CreatedAt.Format("02 January 2006"),
		Status:        string(count.Status),
		WarehouseName: warehouseName,
		CountedBy:     h.countUserLabel(ctx, tenantID, count.CountedBy),
		ApprovedBy:    h.countUserLabel(ctx, tenantID, count.ApprovedBy),
		Items:         items,
		Notes:         nonEmptyStrs(count.Notes),
	}
	if count.ApprovedAt != nil {
		doc.ApprovedAt = count.ApprovedAt.Format("02 January 2006")
	}

	pdfBytes, err := documents.RenderStockCountPDF(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PDF_FAILED", "Failed to render stock count PDF")
		return
	}
	writePDF(w, ifEmptyStr(count.Reference, "stock-count")+"-"+mode, pdfBytes)
}

// defaultStockCountMode picks the document a count's CURRENT status actually supports: once the
// count has been counted there are real numbers to reconcile (variance report); before that the
// only useful document is the blank sheet to go and count with.
func defaultStockCountMode(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "review", "approved", "posted", "completed":
		return documents.StockCountModeVariance
	default:
		return documents.StockCountModeBlank
	}
}

// countUserLabel resolves a stock-count actor to a human name, falling back to empty (not a raw
// UUID) so an unresolved signatory simply leaves the sign-off line blank to hand-sign.
func (h *StockCountHandler) countUserLabel(ctx context.Context, tenantID uuid.UUID, userID *uuid.UUID) string {
	if userID == nil || *userID == uuid.Nil {
		return ""
	}
	if u, e := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(*userID)).Only(ctx); e == nil {
		if u.Name != "" {
			return u.Name
		}
		return u.Email
	}
	return ""
}
