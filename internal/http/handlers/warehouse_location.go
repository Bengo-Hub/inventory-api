package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entwl "github.com/bengobox/inventory-service/internal/ent/warehouselocation"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

type WarehouseLocationHandler struct {
	log     *zap.Logger
	orm     *ent.Client
	rbacSvc *rbac.Service
}

func NewWarehouseLocationHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *WarehouseLocationHandler {
	return &WarehouseLocationHandler{
		log:     log.Named("warehouse_location.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

func (h *WarehouseLocationHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/warehouses/{warehouseID}/locations", func(loc chi.Router) {
		loc.Get("/", h.List)
		loc.With(perm(rbac.PermWarehousesChange)).Post("/", h.Create)
		loc.With(perm(rbac.PermWarehousesChange)).Put("/{locationID}", h.Update)
		loc.With(perm(rbac.PermWarehousesDelete)).Delete("/{locationID}", h.Deactivate)
	})
}

type warehouseLocationDTO struct {
	ID            uuid.UUID  `json:"id"`
	WarehouseID   uuid.UUID  `json:"warehouse_id"`
	ParentID      *uuid.UUID `json:"parent_id,omitempty"`
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Depth         int        `json:"depth"`
	Path          string     `json:"path"`
	IsActive      bool       `json:"is_active"`
	CapacityUnits *int       `json:"capacity_units,omitempty"`
	Notes         string     `json:"notes,omitempty"`
}

type createLocationReq struct {
	ParentID      *uuid.UUID `json:"parent_id"`
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	CapacityUnits *int       `json:"capacity_units"`
	Notes         string     `json:"notes"`
}

type updateLocationReq struct {
	Name          string `json:"name"`
	Notes         string `json:"notes"`
	CapacityUnits *int   `json:"capacity_units"`
	IsActive      *bool  `json:"is_active"`
}

func (h *WarehouseLocationHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	warehouseIDStr := chi.URLParam(r, "warehouseID")
	warehouseID, err := uuid.Parse(warehouseIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE", "Invalid warehouse ID")
		return
	}

	q := h.orm.WarehouseLocation.Query().
		Where(entwl.TenantID(tenantID), entwl.WarehouseID(warehouseID))

	if t := r.URL.Query().Get("type"); t != "" {
		q = q.Where(entwl.TypeEQ(t))
	}
	if pid := r.URL.Query().Get("parent_id"); pid != "" {
		parentID, err := uuid.Parse(pid)
		if err == nil {
			q = q.Where(entwl.ParentIDEQ(parentID))
		}
	}

	locations, err := q.Order(ent.Asc(entwl.FieldDepth), ent.Asc(entwl.FieldCode)).All(r.Context())
	if err != nil {
		h.log.Error("list warehouse locations failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list locations")
		return
	}

	dtos := make([]warehouseLocationDTO, 0, len(locations))
	for _, loc := range locations {
		dtos = append(dtos, toLocationDTO(loc))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (h *WarehouseLocationHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	warehouseIDStr := chi.URLParam(r, "warehouseID")
	warehouseID, err := uuid.Parse(warehouseIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE", "Invalid warehouse ID")
		return
	}

	var req createLocationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Code == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "code and name are required")
		return
	}

	depth := 0
	path := fmt.Sprintf("/%s/", strings.ToLower(req.Code))
	locType := req.Type
	if locType == "" {
		locType = "bin"
	}

	if req.ParentID != nil {
		parent, err := h.orm.WarehouseLocation.Get(r.Context(), *req.ParentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PARENT_NOT_FOUND", "Parent location not found")
			return
		}
		depth = parent.Depth + 1
		parentPath := parent.Path
		if parentPath == "" {
			parentPath = "/"
		}
		path = parentPath + strings.ToLower(req.Code) + "/"
	}

	creator := h.orm.WarehouseLocation.Create().
		SetTenantID(tenantID).
		SetWarehouseID(warehouseID).
		SetCode(req.Code).
		SetName(req.Name).
		SetType(locType).
		SetDepth(depth).
		SetPath(path)

	if req.ParentID != nil {
		creator = creator.SetParentID(*req.ParentID)
	}
	if req.CapacityUnits != nil {
		creator = creator.SetCapacityUnits(*req.CapacityUnits)
	}
	if req.Notes != "" {
		creator = creator.SetNotes(req.Notes)
	}

	loc, err := creator.Save(r.Context())
	if err != nil {
		h.log.Error("create warehouse location failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create location")
		return
	}
	writeJSON(w, http.StatusCreated, toLocationDTO(loc))
}

func (h *WarehouseLocationHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	locationIDStr := chi.URLParam(r, "locationID")
	locationID, err := uuid.Parse(locationIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_LOCATION", "Invalid location ID")
		return
	}

	var req updateLocationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	loc, err := h.orm.WarehouseLocation.Get(r.Context(), locationID)
	if err != nil || loc.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Location not found")
		return
	}

	upd := h.orm.WarehouseLocation.UpdateOneID(locationID)
	if req.Name != "" {
		upd = upd.SetName(req.Name)
	}
	if req.Notes != "" {
		upd = upd.SetNotes(req.Notes)
	}
	if req.CapacityUnits != nil {
		upd = upd.SetCapacityUnits(*req.CapacityUnits)
	}
	if req.IsActive != nil {
		upd = upd.SetIsActive(*req.IsActive)
	}

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update warehouse location failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update location")
		return
	}
	writeJSON(w, http.StatusOK, toLocationDTO(updated))
}

func (h *WarehouseLocationHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	locationIDStr := chi.URLParam(r, "locationID")
	locationID, err := uuid.Parse(locationIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_LOCATION", "Invalid location ID")
		return
	}

	loc, err := h.orm.WarehouseLocation.Get(r.Context(), locationID)
	if err != nil || loc.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Location not found")
		return
	}

	if _, err := h.orm.WarehouseLocation.UpdateOneID(locationID).SetIsActive(false).Save(r.Context()); err != nil {
		h.log.Error("deactivate warehouse location failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DEACTIVATE_FAILED", "Failed to deactivate location")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}

func toLocationDTO(loc *ent.WarehouseLocation) warehouseLocationDTO {
	dto := warehouseLocationDTO{
		ID:          loc.ID,
		WarehouseID: loc.WarehouseID,
		Code:        loc.Code,
		Name:        loc.Name,
		Type:        loc.Type,
		Depth:       loc.Depth,
		Path:        loc.Path,
		IsActive:    loc.IsActive,
		Notes:       loc.Notes,
	}
	if loc.ParentID != nil {
		dto.ParentID = loc.ParentID
	}
	if loc.CapacityUnits != nil {
		dto.CapacityUnits = loc.CapacityUnits
	}
	return dto
}
