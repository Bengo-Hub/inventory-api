package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── Document numbering settings ────────────────────────────────────────────
// Per-tenant, per-doc-type sequence configuration (prefix / date format / padding)
// that drives PO, GRN and other inventory document numbers. Mirrors treasury-ui's
// document-numbering settings.

func (h *InventoryExtrasHandler) registerDocumentSequenceRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, manage string) {
	r.Get("/inventory/document-sequences", h.ListDocumentSequences)
	r.Get("/inventory/document-sequences/{docType}/preview", h.PreviewDocumentNumber)
	r.With(perm(manage)).Put("/inventory/document-sequences/{docType}", h.UpdateDocumentSequence)
}

// ListDocumentSequences returns every inventory doc type's sequence config (auto-seeded).
func (h *InventoryExtrasHandler) ListDocumentSequences(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.docSvc == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}})
		return
	}
	rows, err := h.docSvc.Seq().ListConfigs(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load document sequences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}

// PreviewDocumentNumber returns the next number for a doc type without consuming it.
func (h *InventoryExtrasHandler) PreviewDocumentNumber(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "NO_DOC_SVC", "Document service unavailable")
		return
	}
	n, err := h.docSvc.Seq().PreviewNext(r.Context(), tenantID, chi.URLParam(r, "docType"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to preview number")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"next_number": n})
}

// UpdateDocumentSequence updates the format config (prefix/date/padding) for a doc type.
func (h *InventoryExtrasHandler) UpdateDocumentSequence(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "NO_DOC_SVC", "Document service unavailable")
		return
	}
	var cfg documents.SeqConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	out, err := h.docSvc.Seq().UpdateConfig(r.Context(), tenantID, chi.URLParam(r, "docType"), cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update document sequence")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
