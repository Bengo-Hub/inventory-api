package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ensureAdjustmentReference mints a stock-adjustment document number when the caller supplied
// no reference of their own, so a batch of corrections has ONE stable identifier to print,
// file and audit against (GET /inventory/adjustments/document?reference=…).
//
// Never overwrites a user-supplied reference — a merchant who types "SPOILAGE-JUNE" or pastes a
// supplier's credit note number keeps exactly that. Best-effort: on any numbering failure the
// reference simply stays blank and the adjustment still applies, since a document number is not
// worth failing a stock correction over.
func (h *InventoryHandler) ensureAdjustmentReference(ctx context.Context, tenantID uuid.UUID, current string) string {
	if strings.TrimSpace(current) != "" || h.docSvc == nil {
		return current
	}
	n, err := h.docSvc.Seq().GenerateNumber(ctx, tenantID, documents.DocTypeStockAdjustment)
	if err != nil || n == "" {
		h.log.Debug("stock adjustment reference generation skipped", zap.Error(err))
		return current
	}
	return n
}

// GenerateStockAdjustmentDocument renders the signed audit note for a BATCH of stock adjustments
// (GET /inventory/adjustments/document?reference=XXX).
//
// Keyed by query param rather than an id because a StockAdjustment row is a per-line audit
// record with no document identity of its own — the "document" is the virtual grouping of every
// row sharing one `reference` (auto-minted at creation when the caller supplied none, see
// ensureAdjustmentReference). Optionally narrowed further with warehouse_id when the same
// reference was used across warehouses.
//
//	@Summary      Generate a stock adjustment note for a reference batch
//	@Tags         stock
//	@Produce      application/pdf
//	@Param        reference     query     string  true   "Adjustment batch reference"
//	@Param        warehouse_id  query     string  false  "Narrow the batch to one warehouse"
//	@Success      200           {file}    binary
//	@Failure      400           {object}  map[string]string
//	@Failure      404           {object}  map[string]string
//	@Failure      500           {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/adjustments/document [get]
func (h *InventoryHandler) GenerateStockAdjustmentDocument(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	format, ok := docFormatFromQuery(w, r)
	if !ok {
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		writeError(w, http.StatusBadRequest, "MISSING_REFERENCE", "reference is required")
		return
	}
	ctx := r.Context()

	q := h.orm.StockAdjustment.Query().
		Where(entadj.TenantID(tenantID), entadj.ReferenceEQ(reference))
	if whStr := strings.TrimSpace(r.URL.Query().Get("warehouse_id")); whStr != "" {
		whID, perr := uuid.Parse(whStr)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE_ID", "Invalid warehouse_id")
			return
		}
		q = q.Where(entadj.WarehouseID(whID))
	}
	rows, err := q.Order(ent.Asc(entadj.FieldAdjustedAt)).All(ctx)
	if err != nil {
		h.log.Error("load stock adjustment batch failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load adjustments")
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "No adjustments found for that reference")
		return
	}

	// Item identifiers so the note never prints raw UUIDs.
	itemIDs := make([]uuid.UUID, 0, len(rows))
	for _, a := range rows {
		itemIDs = append(itemIDs, a.ItemID)
	}
	names, skus := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	if items, e := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).All(ctx); e == nil {
		for _, it := range items {
			names[it.ID] = it.Name
			skus[it.ID] = it.Sku
		}
	}

	// One document = one batch, so the header takes the batch's first row for warehouse/actor.
	// A reference reused across warehouses (without the warehouse_id filter) still lists every
	// row; the header names the first, which is why the filter exists.
	warehouseName := ""
	if wh, e := h.orm.Warehouse.Query().
		Where(entwarehouse.ID(rows[0].WarehouseID), entwarehouse.TenantID(tenantID)).Only(ctx); e == nil {
		warehouseName = wh.Name
	}

	items := make([]documents.StockAdjustmentDocLine, 0, len(rows))
	var batchNotes []string
	seenNote := map[string]struct{}{}
	for _, a := range rows {
		items = append(items, documents.StockAdjustmentDocLine{
			Desc:    ifEmptyStr(names[a.ItemID], a.ItemID.String()),
			SubDesc: joinDetail(skus[a.ItemID], a.Notes),
			Before:  formatQty(a.QuantityBefore),
			Change:  signedQty(a.QuantityChange),
			After:   formatQty(a.QuantityAfter),
			Reason:  string(a.Reason),
		})
		// A batch usually shares one note across its rows — dedupe so the notes card doesn't
		// repeat the same sentence once per line.
		if n := strings.TrimSpace(a.Notes); n != "" {
			if _, dup := seenNote[n]; !dup {
				seenNote[n] = struct{}{}
				batchNotes = append(batchNotes, n)
			}
		}
	}

	doc := documents.StockAdjustmentDoc{
		Branding:      h.docSvc.GetBranding(ctx, tenantID),
		Reference:     reference,
		Date:          rows[0].AdjustedAt.Format("02 January 2006"),
		WarehouseName: warehouseName,
		AdjustedBy:    h.adjusterLabel(ctx, tenantID, rows[0].AdjustedBy),
		Items:         items,
		Notes:         batchNotes,
	}

	var fileBytes []byte
	switch format {
	case "csv":
		fileBytes, err = documents.RenderStockAdjustmentCSV(doc)
	case "xlsx":
		fileBytes, err = documents.RenderStockAdjustmentXLSX(doc)
	default:
		fileBytes, err = documents.RenderStockAdjustmentPDF(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOC_RENDER_FAILED", "Failed to render stock adjustment document")
		return
	}
	writeDocFile(w, reference, format, fileBytes)
}

// adjusterLabel resolves the acting user's display name, falling back to empty (rather than a
// raw UUID) so the sign-off line simply shows a blank to hand-sign.
func (h *InventoryHandler) adjusterLabel(ctx context.Context, tenantID, userID uuid.UUID) string {
	if userID == uuid.Nil || h.orm == nil {
		return ""
	}
	if u, e := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserID(userID)).Only(ctx); e == nil {
		if u.Name != "" {
			return u.Name
		}
		return u.Email
	}
	return ""
}

// actorNamesByID batch-resolves display names for a set of actor (auth-service) user ids in one
// query — the list-view counterpart of adjusterLabel (which resolves one id for a printed
// document's sign-off line). Used anywhere a list DTO carries a raw "who did this" UUID
// (adjusted_by, received_by, initiated_by…) that the UI should render as a name, not a UUID.
// Missing/zero ids are simply absent from the returned map. A package-level function (not a
// method) so every handler struct in this package can share it without a common base type.
func actorNamesByID(ctx context.Context, orm *ent.Client, tenantID uuid.UUID, ids []uuid.UUID) map[uuid.UUID]string {
	names := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 || orm == nil {
		return names
	}
	users, err := orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.AuthServiceUserIDIn(ids...)).
		All(ctx)
	if err != nil {
		return names
	}
	for _, u := range users {
		if u.Name != "" {
			names[u.AuthServiceUserID] = u.Name
		} else {
			names[u.AuthServiceUserID] = u.Email
		}
	}
	return names
}

// signedQty formats a quantity delta with an explicit sign, so "+3" and "-12" read unambiguously
// in the CHANGE column.
func signedQty(q float64) string {
	if q > 0 {
		return "+" + formatQty(q)
	}
	return formatQty(q)
}
