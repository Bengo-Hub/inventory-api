package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/backup"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// Backups serves tenant-scoped backups (this tenant's data only). The artifact is a
// gzipped-JSON export of the tenant's rows, tracked in the `backups` table. Reads require
// inventory.settings.view; mutations + churn require inventory.settings.manage.
type Backups struct {
	log           *zap.Logger
	svc           *backup.Service
	rbacSvc       *rbac.Service
	retentionDays int
}

// NewBackups builds the handler.
func NewBackups(log *zap.Logger, svc *backup.Service, rbacSvc *rbac.Service, retentionDays int) *Backups {
	if retentionDays <= 0 {
		retentionDays = backup.DefaultRetentionDays
	}
	return &Backups{log: log.Named("backups"), svc: svc, rbacSvc: rbacSvc, retentionDays: retentionDays}
}

func (h *Backups) perm(code string) func(http.Handler) http.Handler {
	if h.rbacSvc == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
}

// RegisterRoutes mounts the backup endpoints on the given (already tenant-scoped, auth'd)
// router under /backups.
func (h *Backups) RegisterRoutes(r chi.Router) {
	r.Route("/backups", func(br chi.Router) {
		br.With(h.perm(rbac.PermSettingsView)).Get("/", h.List)
		br.With(h.perm(rbac.PermSettingsManage)).Post("/", h.Create)
		br.With(h.perm(rbac.PermSettingsView)).Get("/settings", h.GetSettings)
		br.With(h.perm(rbac.PermSettingsManage)).Put("/settings", h.UpdateSettings)
		br.With(h.perm(rbac.PermSettingsManage)).Post("/churn", h.Churn)
		br.With(h.perm(rbac.PermSettingsView)).Get("/{name}/download", h.Download)
		br.With(h.perm(rbac.PermSettingsManage)).Delete("/{name}", h.Delete)
	})
}

// GetSettings returns the tenant's auto-backup settings (auto_enabled defaults to false).
func (h *Backups) GetSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	settings, err := h.svc.GetSettings(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load backup settings")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

type backupSettingsBody struct {
	AutoEnabled   bool `json:"auto_enabled"`
	ScheduleHour  int  `json:"schedule_hour"`
	RetentionDays int  `json:"retention_days"`
}

// UpdateSettings activates/deactivates auto-backup + sets schedule hour + retention.
func (h *Backups) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	var b backupSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	settings, err := h.svc.UpsertSettings(r.Context(), tenantID, backup.Settings{
		AutoEnabled: b.AutoEnabled, ScheduleHour: b.ScheduleHour, RetentionDays: b.RetentionDays,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", "failed to save backup settings")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Backups) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	items, err := h.svc.List(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list backups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": items})
}

func (h *Backups) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	info, err := h.svc.Generate(r.Context(), tenantID)
	if err != nil {
		h.log.Error("generate backup", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "backup_failed", "failed to generate backup")
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// Churn manually triggers retention cleanup (deletes files + tracking rows older than the
// configured retention window across the cluster). The daily scheduler runs this too.
func (h *Backups) Churn(w http.ResponseWriter, r *http.Request) {
	removed, err := h.svc.Churn(r.Context(), h.retentionDays)
	if err != nil {
		h.log.Error("churn backups", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "churn_failed", "failed to churn backups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "retention_days": h.retentionDays})
}

func (h *Backups) Download(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	name := chi.URLParam(r, "name")
	rc, err := h.svc.Open(tenantID, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "backup not found")
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (h *Backups) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := ResolveTenantForRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "tenant_required", "tenant context required")
		return
	}
	if err := h.svc.Delete(r.Context(), tenantID, chi.URLParam(r, "name")); err != nil {
		writeError(w, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
