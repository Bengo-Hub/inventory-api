package handlers

import (
	"net/http"
	"path"

	"github.com/Bengo-Hub/httpware/objectstore"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"go.uber.org/zap"
)

// mediaUploadTypes are the media types the generic upload accepts (query "type"): item and
// menu pictures (the default), brand and supplier logos, and other catalogue pictures.
var mediaUploadTypes = map[string]bool{
	objectstore.TypeItemImages: true,
	objectstore.TypeMenuImages: true,
	objectstore.TypeLogos:      true,
	objectstore.TypeImages:     true,
}

// MediaHandler handles single image uploads (items in create mode, brand and supplier logos).
type MediaHandler struct {
	log   *zap.Logger
	store *objectstore.Store
}

// NewMediaHandler creates a new media handler on the shared object storage.
func NewMediaHandler(log *zap.Logger, store *objectstore.Store) *MediaHandler {
	return &MediaHandler{log: log.Named("media.handler"), store: store}
}

// Upload stores one image for the caller's tenant: JPEG or PNG within the type's limit, checked from the bytes
// and re-encoded (EXIF and anything appended removed). It answers the image's link ("url", what a
// form puts in image_url or logo_url) and its key; saving either stores the key.
//
// @Summary Upload an image
// @Tags Media
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "JPEG or PNG, up to 2 MB (5 MB for images)"
// @Param type query string false "item-images (default), menu-images, logos or images"
// @Success 201 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Router /api/v1/media/upload [post]
func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" {
		writeError(w, http.StatusBadRequest, "TENANT_REQUIRED", "Sign in to an organisation to upload")
		return
	}
	if !h.store.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "File storage is not configured")
		return
	}
	// The type decides the limits, so it is read before the body (query string).
	mediaType := r.URL.Query().Get("type")
	if mediaType == "" {
		mediaType = objectstore.TypeItemImages
	}
	if !mediaUploadTypes[mediaType] {
		writeError(w, http.StatusBadRequest, "INVALID_TYPE", "type must be item-images, menu-images, logos or images")
		return
	}
	up, err := objectstore.ReadUpload(w, r, "file", mediaType)
	if err != nil {
		if objectstore.IsUploadError(err) {
			writeError(w, http.StatusBadRequest, "INVALID_FILE", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to read the upload")
		return
	}
	key, err := h.store.NewKey(objectstore.Owner{TenantID: claims.TenantID}, mediaType, up.Ext)
	if err != nil {
		writeError(w, http.StatusBadRequest, "TENANT_REQUIRED", "Invalid organisation")
		return
	}
	if err := h.store.Put(r.Context(), key, up.Data, up.ContentType); err != nil {
		h.log.Error("media upload not stored", zap.Error(err))
		writeError(w, http.StatusBadGateway, "STORAGE_UNAVAILABLE", "File storage is unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"url":      h.store.URL(key),
		"key":      key,
		"filename": path.Base(key),
	})
}
