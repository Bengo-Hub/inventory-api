package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

// maxImageUpload caps a single item-image upload at 2MB (mirrors the media handler).
const maxImageUpload = 2 * 1024 * 1024

// ListItemImages handles GET /{tenant}/inventory/items/{itemID}/images.
// Returns the item's IMAGE assets (primary first, then display_order). No subscription gate.
func (h *InventoryHandler) ListItemImages(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item ID")
		return
	}
	imgs, err := h.itemsSvc.ListItemImages(r.Context(), tenantID, itemID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("list item images failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list images")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": imgs})
}

// UploadItemImage handles POST /{tenant}/inventory/items/{itemID}/images.
// Multipart "file" upload. Subscription-gated: the 2nd+ image requires the
// inventory_multiple_images feature, and the per-item IMAGE count is capped by
// inventory_max_images_per_item (default 1). Optional ?set_primary / is_primary form value.
func (h *InventoryHandler) UploadItemImage(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item ID")
		return
	}

	// Current IMAGE count drives both gates (feature only bites beyond the first image).
	currentCount, err := h.itemsSvc.CountItemImages(r.Context(), tenantID, itemID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("count item images failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to read images")
		return
	}

	// The first image is always allowed (legacy single-image parity). Only the 2nd+ image is
	// gated behind the multiple-images feature and the per-item image cap.
	if currentCount >= 1 {
		if subscriptions.AssertFeature(w, r, subscriptions.FeatureMultipleImages, "multiple product images") {
			return // 403 feature-lock written
		}
		if subscriptions.AssertImageLimit(w, r, currentCount) {
			return // 402 over per-item image cap written
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImageUpload)
	if err := r.ParseMultipartForm(maxImageUpload); err != nil {
		writeError(w, http.StatusBadRequest, "UPLOAD_TOO_LARGE", "File too large (max 2MB)")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FILE", "Invalid file upload")
		return
	}
	defer file.Close()

	setPrimary := parseBoolForm(r.FormValue("set_primary")) || parseBoolForm(r.FormValue("is_primary"))

	img, err := h.itemsSvc.AddItemImage(r.Context(), tenantID, itemID, file, header, setPrimary)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		if strings.Contains(err.Error(), "allowed") {
			writeError(w, http.StatusBadRequest, "INVALID_TYPE", "Only JPEG, JPG and PNG images are allowed")
			return
		}
		h.log.Error("add item image failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPLOAD_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, img)
}

// UpdateItemImage handles PATCH /{tenant}/inventory/items/{itemID}/images/{imageID}.
// Reorders (display_order) and/or sets the image as primary (unsets others). No subscription gate.
func (h *InventoryHandler) UpdateItemImage(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item ID")
		return
	}
	imageID, err := uuid.Parse(chi.URLParam(r, "imageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid image ID")
		return
	}
	var in items.UpdateItemImageInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	img, err := h.itemsSvc.UpdateItemImage(r.Context(), tenantID, itemID, imageID, in)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Image not found")
			return
		}
		h.log.Error("update item image failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, img)
}

// DeleteItemImage handles DELETE /{tenant}/inventory/items/{itemID}/images/{imageID}.
// When the primary image is removed, the next image is promoted to primary.
func (h *InventoryHandler) DeleteItemImage(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item ID")
		return
	}
	imageID, err := uuid.Parse(chi.URLParam(r, "imageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid image ID")
		return
	}
	if err := h.itemsSvc.DeleteItemImage(r.Context(), tenantID, itemID, imageID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Image not found")
			return
		}
		h.log.Error("delete item image failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// parseBoolForm parses a permissive boolean form value ("1", "true", "yes", "on").
func parseBoolForm(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
