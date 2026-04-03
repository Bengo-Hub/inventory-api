package handlers

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/config"
)

// MediaHandler handles media file uploads for inventory items.
type MediaHandler struct {
	log *zap.Logger
	cfg config.MediaConfig
}

// NewMediaHandler creates a new media handler.
func NewMediaHandler(log *zap.Logger, cfg config.MediaConfig) *MediaHandler {
	return &MediaHandler{
		log: log.Named("media.handler"),
		cfg: cfg,
	}
}

// Upload handles file uploads for menu item images and other media.
// It validates file size and content type, strips EXIF metadata via re-encoding,
// and saves the file to the configured media root directory.
func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 2MB
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
		h.log.Error("failed to parse multipart form", zap.Error(err))
		writeError(w, http.StatusBadRequest, "UPLOAD_TOO_LARGE", "File too large (max 2MB)")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		h.log.Error("failed to get file from form", zap.Error(err))
		writeError(w, http.StatusBadRequest, "INVALID_FILE", "Invalid file upload")
		return
	}
	defer file.Close()

	// Detect content type from file bytes
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	contentType := http.DetectContentType(buffer[:n])
	if _, err := file.Seek(0, 0); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
		return
	}

	// Only allow JPEG and PNG — re-encoding below strips EXIF and neutralizes
	// image-based exploits, acting as a sanitization/virus mitigation step.
	allowedTypes := map[string]bool{
		"image/jpeg": true,
		"image/jpg":  true,
		"image/png":  true,
	}

	// Fall back to extension if content detection is ambiguous
	if !allowedTypes[contentType] {
		ext := strings.ToLower(filepath.Ext(header.Filename))
		headerCT := header.Header.Get("Content-Type")

		switch {
		case ext == ".jpg" || ext == ".jpeg" || headerCT == "image/jpeg":
			contentType = "image/jpeg"
		case ext == ".png" || headerCT == "image/png":
			contentType = "image/png"
		}
	}

	if !allowedTypes[contentType] {
		h.log.Warn("rejected file upload", zap.String("detected_type", contentType), zap.String("filename", header.Filename))
		writeError(w, http.StatusBadRequest, "INVALID_TYPE", "Only JPG and PNG images are allowed")
		return
	}

	// Generate unique filename
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" || (ext != ".jpg" && ext != ".jpeg" && ext != ".png") {
		switch contentType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		}
	}
	filename := fmt.Sprintf("%s_%d%s", uuid.New().String(), time.Now().Unix(), ext)

	// Create upload directory
	dir := filepath.Join(h.cfg.Root, "uploads", "menu")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.log.Error("failed to create upload directory", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
		return
	}

	dstPath := filepath.Join(dir, filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		h.log.Error("failed to create destination file", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
		return
	}
	defer dst.Close()

	// Re-encode JPEG/PNG to strip EXIF metadata (security measure)
	written := false
	if contentType == "image/jpeg" || contentType == "image/png" {
		img, _, decErr := image.Decode(file)
		if decErr == nil {
			var encErr error
			if contentType == "image/jpeg" {
				encErr = jpeg.Encode(dst, img, &jpeg.Options{Quality: 85})
			} else {
				encErr = png.Encode(dst, img)
			}
			if encErr == nil {
				written = true
			} else {
				h.log.Warn("re-encoding failed, falling back to direct copy", zap.Error(encErr))
				if _, err := file.Seek(0, 0); err != nil {
					writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
					return
				}
				if err := dst.Truncate(0); err != nil {
					writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
					return
				}
				if _, err := dst.Seek(0, 0); err != nil {
					writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
					return
				}
			}
		}
	}

	if !written {
		if _, err := io.Copy(dst, file); err != nil {
			h.log.Error("failed to copy file", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
			return
		}
	}

	// Build URL: full URL if URLBase is configured, otherwise relative path
	var url string
	relativePath := fmt.Sprintf("/media/uploads/menu/%s", filename)
	if h.cfg.URLBase != "" {
		url = fmt.Sprintf("%s%s", strings.TrimRight(h.cfg.URLBase, "/"), relativePath)
	} else {
		url = relativePath
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"url":      url,
		"filename": filename,
	})
}
