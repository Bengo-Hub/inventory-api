package handlers

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GenerateBundleSpecPDF renders a bundle/hospitality-package spec sheet
// (GET /inventory/bundles/{bundleID}/spec.pdf).
//
// A bundle is MASTER DATA, not a transaction — no date, no counterparty, no document number —
// so unlike every other document endpoint here it consumes no document sequence.
//
//	@Summary      Generate a bundle/package spec sheet PDF
//	@Tags         Inventory
//	@Produce      application/pdf
//	@Param        bundleID  path      string  true  "Bundle ID"
//	@Success      200       {file}    binary
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Failure      500       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/bundles/{bundleID}/spec.pdf [get]
func (h *InventoryExtrasHandler) GenerateBundleSpecPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
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
	bundleID, err := uuid.Parse(chi.URLParam(r, "bundleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid bundle ID")
		return
	}
	ctx := r.Context()
	b, err := h.bundleSvc.GetBundle(ctx, tenantID, bundleID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Bundle not found")
		return
	}

	components := make([]documents.BundleSpecDocLine, 0, len(b.Components))
	for _, c := range b.Components {
		line := documents.BundleSpecDocLine{
			Desc:    ifEmptyStr(c.ItemName, c.ComponentItemID.String()),
			SubDesc: c.ItemSKU,
			Kind:    c.ComponentKind,
			Qty:     fmt.Sprintf("%d", c.Quantity),
			Unit:    c.Unit,
			Metered: c.IsMetered,
		}
		if c.MealPeriod != nil {
			line.MealPeriod = *c.MealPeriod
		}
		components = append(components, line)
	}

	// Only the optional package attributes that actually apply to THIS bundle — a retail kit
	// shouldn't print empty conference/hospitality rows.
	var attrs [][2]string
	if b.MinDelegates != nil && *b.MinDelegates > 0 {
		attrs = append(attrs, [2]string{"Min Delegates", fmt.Sprintf("%d", *b.MinDelegates)})
	}
	if b.SessionsTotal != nil && *b.SessionsTotal > 0 {
		attrs = append(attrs, [2]string{"Sessions", fmt.Sprintf("%d", *b.SessionsTotal)})
	}
	if b.ValidityDays != nil && *b.ValidityDays > 0 {
		attrs = append(attrs, [2]string{"Validity", fmt.Sprintf("%d days", *b.ValidityDays)})
	}
	if b.AccommodationIncluded {
		attrs = append(attrs, [2]string{"Accommodation", "INCLUDED"})
	}

	doc := documents.BundleSpecDoc{
		Branding:    h.docSvc.GetBranding(ctx, tenantID),
		BundleName:  b.Name,
		ItemName:    b.ItemName,
		ItemSKU:     h.itemSKU(ctx, tenantID, b.ItemID, nil),
		PackageType: b.PackageType,
		PriceBasis:  b.PriceBasis,
		Attributes:  attrs,
		IsActive:    b.IsActive,
		Components:  components,
	}

	var fileBytes []byte
	switch format {
	case "csv":
		fileBytes, err = documents.RenderBundleSpecCSV(doc)
	case "xlsx":
		fileBytes, err = documents.RenderBundleSpecXLSX(doc)
	default:
		fileBytes, err = documents.RenderBundleSpecPDF(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOC_RENDER_FAILED", "Failed to render bundle spec document")
		return
	}
	writeDocFile(w, ifEmptyStr(b.Name, "bundle-spec"), format, fileBytes)
}
