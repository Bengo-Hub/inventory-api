package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/modules/bundles"
)

// ─── Bundles ─────────────────────────────────────────────────────────────────

// ListBundles returns paginated bundles for the tenant.
func (h *InventoryExtrasHandler) ListBundles(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
		return
	}
	p := pagination.Parse(r)
	result, total, err := h.bundleSvc.ListBundles(r.Context(), tenantID, p.Limit, p.Offset)
	if err != nil {
		h.log.Error("list bundles failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list bundles")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}

// GetBundle returns a single bundle by ID.
func (h *InventoryExtrasHandler) GetBundle(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
		return
	}
	bundleID, err := uuid.Parse(chi.URLParam(r, "bundleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid bundle ID")
		return
	}
	b, err := h.bundleSvc.GetBundle(r.Context(), tenantID, bundleID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Bundle not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// CreateBundle creates a new product bundle.
func (h *InventoryExtrasHandler) CreateBundle(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
		return
	}
	var dto bundles.BundleDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if dto.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Bundle name is required")
		return
	}
	created, err := h.bundleSvc.CreateBundle(r.Context(), tenantID, dto)
	if err != nil {
		h.log.Error("create bundle failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to create bundle")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// UpdateBundle replaces a bundle's metadata and components.
func (h *InventoryExtrasHandler) UpdateBundle(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
		return
	}
	bundleID, err := uuid.Parse(chi.URLParam(r, "bundleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid bundle ID")
		return
	}
	var dto bundles.BundleDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	updated, err := h.bundleSvc.UpdateBundle(r.Context(), tenantID, bundleID, dto)
	if err != nil {
		h.log.Error("update bundle failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update bundle")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteBundle removes a bundle.
func (h *InventoryExtrasHandler) DeleteBundle(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.bundleSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Bundle service not initialized")
		return
	}
	bundleID, err := uuid.Parse(chi.URLParam(r, "bundleID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid bundle ID")
		return
	}
	if err := h.bundleSvc.DeleteBundle(r.Context(), tenantID, bundleID); err != nil {
		h.log.Error("delete bundle failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to delete bundle")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
