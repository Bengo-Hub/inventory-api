package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// WarehouseHandler handles warehouse listing and outlet config endpoints.
type WarehouseHandler struct {
	log     *zap.Logger
	orm     *ent.Client
	rbacSvc *rbac.Service
	authURL string // base URL of auth-api, e.g. "https://sso.codevertexitsolutions.com"
}

// NewWarehouseHandler creates a warehouse handler.
func NewWarehouseHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *WarehouseHandler {
	return &WarehouseHandler{
		log:     log.Named("warehouse.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

// SetAuthURL sets the auth-api base URL used for the outlet proxy endpoint.
func (h *WarehouseHandler) SetAuthURL(url string) {
	h.authURL = strings.TrimRight(url, "/")
}

// RegisterRoutes wires warehouse routes onto the given chi.Router.
func (h *WarehouseHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory/warehouses", func(wh chi.Router) {
		wh.Get("/", h.ListWarehouses)
		wh.With(perm(rbac.PermWarehousesAdd)).Post("/", h.CreateWarehouse)
		wh.Get("/{warehouseID}", h.GetWarehouse)
		wh.With(perm(rbac.PermWarehousesChange)).Put("/{warehouseID}", h.UpdateWarehouse)
		wh.With(perm(rbac.PermWarehousesDelete)).Delete("/{warehouseID}", h.DeleteWarehouse)
	})

	// Outlet listing — proxies to auth-api so the UI has a single base URL.
	r.Get("/inventory/outlets", h.ListOutlets)
	// Outlet config: assign a default warehouse to an outlet + set use_case metadata.
	r.With(perm(rbac.PermConfigChange)).Put("/inventory/outlets/{outletID}/config", h.UpdateOutletConfig)
	r.Get("/inventory/outlets/{outletID}/config", h.GetOutletConfig)
}

// outletConfigDTO is the request/response body for outlet warehouse config.
type outletConfigDTO struct {
	DefaultWarehouseID *uuid.UUID `json:"default_warehouse_id,omitempty"`
	UseCase            string     `json:"use_case,omitempty"`
}

// UpdateOutletConfig handles PUT /{tenant}/inventory/outlets/{outletID}/config.
// Sets the default warehouse for an outlet and optionally stores use_case metadata.
func (h *WarehouseHandler) UpdateOutletConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	outletIDStr := chi.URLParam(r, "outletID")
	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_OUTLET", "Invalid outlet ID")
		return
	}

	var req outletConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	ctx := r.Context()

	if req.DefaultWarehouseID != nil {
		// Verify warehouse belongs to this tenant, then assign outlet_id.
		wh, err := h.orm.Warehouse.
			Query().
			Where(
				entwarehouse.TenantID(tenantID),
				entwarehouse.ID(*req.DefaultWarehouseID),
			).
			Only(ctx)
		if err != nil {
			writeError(w, http.StatusNotFound, "WAREHOUSE_NOT_FOUND", "Warehouse not found for tenant")
			return
		}

		// Clear any existing outlet assignment for this outlet (one outlet → one default warehouse).
		_, _ = h.orm.Warehouse.
			Update().
			Where(
				entwarehouse.TenantID(tenantID),
				entwarehouse.OutletID(outletID),
			).
			ClearOutletID().
			Save(ctx)

		_, err = h.orm.Warehouse.
			UpdateOneID(wh.ID).
			SetOutletID(outletID).
			Save(ctx)
		if err != nil {
			h.log.Error("failed to set outlet warehouse", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update warehouse outlet assignment")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"outlet_id":            outletID,
		"default_warehouse_id": req.DefaultWarehouseID,
		"use_case":             req.UseCase,
	})
}

// GetOutletConfig handles GET /{tenant}/inventory/outlets/{outletID}/config.
func (h *WarehouseHandler) GetOutletConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	outletIDStr := chi.URLParam(r, "outletID")
	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_OUTLET", "Invalid outlet ID")
		return
	}

	ctx := r.Context()

	wh, err := h.orm.Warehouse.
		Query().
		Where(
			entwarehouse.TenantID(tenantID),
			entwarehouse.OutletID(outletID),
		).
		First(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"outlet_id":            outletID,
			"default_warehouse_id": nil,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"outlet_id":            outletID,
		"default_warehouse_id": wh.ID,
		"warehouse_name":       wh.Name,
		"warehouse_code":       wh.Code,
	})
}

// ListOutlets handles GET /inventory/outlets.
// It proxies to auth-api so the frontend only needs one base URL.
func (h *WarehouseHandler) ListOutlets(w http.ResponseWriter, r *http.Request) {
	if h.authURL == "" {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "Auth service URL not configured")
		return
	}

	// Resolve tenant slug from request context or URL param.
	slug := chi.URLParam(r, "tenant")
	if slug == "" {
		slug = r.Header.Get("X-Tenant-Slug")
	}
	if slug == "" {
		writeError(w, http.StatusBadRequest, "MISSING_TENANT", "Tenant slug required")
		return
	}

	url := fmt.Sprintf("%s/api/v1/tenants/%s/outlets", h.authURL, slug)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		h.log.Error("build outlet proxy request failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to build request")
		return
	}
	// Forward the caller's auth token so auth-api can scope to the tenant.
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.log.Error("outlet proxy request failed", zap.Error(err))
		writeError(w, http.StatusBadGateway, "PROXY_ERROR", "Failed to fetch outlets")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// warehouseDTO is the request body for creating/updating a warehouse.
type warehouseDTO struct {
	Name      string     `json:"name"`
	Code      string     `json:"code"`
	Address   string     `json:"address"`
	Latitude  *float64   `json:"latitude,omitempty"`
	Longitude *float64   `json:"longitude,omitempty"`
	IsDefault bool       `json:"is_default"`
	IsActive  *bool      `json:"is_active,omitempty"`
	OutletID  *uuid.UUID `json:"outlet_id,omitempty"`
}

type warehouseResponse struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	Name      string     `json:"name"`
	Code      string     `json:"code"`
	Address   string     `json:"address,omitempty"`
	Latitude  *float64   `json:"latitude,omitempty"`
	Longitude *float64   `json:"longitude,omitempty"`
	IsDefault bool       `json:"is_default"`
	IsActive  bool       `json:"is_active"`
	OutletID  *uuid.UUID `json:"outlet_id,omitempty"`
}

func warehouseToResponse(w *ent.Warehouse) warehouseResponse {
	return warehouseResponse{
		ID:        w.ID,
		TenantID:  w.TenantID,
		Name:      w.Name,
		Code:      w.Code,
		Address:   w.Address,
		Latitude:  w.Latitude,
		Longitude: w.Longitude,
		IsDefault: w.IsDefault,
		IsActive:  w.IsActive,
		OutletID:  w.OutletID,
	}
}

// ListWarehouses handles GET /{tenant}/inventory/warehouses.
func (h *WarehouseHandler) ListWarehouses(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	ctx := r.Context()

	q := h.orm.Warehouse.Query().Where(entwarehouse.TenantID(tenantID))

	// Optional outlet filter from X-Outlet-ID header.
	outletIDStr := invmiddleware.GetOutletID(ctx)
	if outletIDStr != "" {
		if outletID, parseErr := uuid.Parse(outletIDStr); parseErr == nil {
			q = q.Where(entwarehouse.OutletID(outletID))
		}
	}

	warehouses, err := q.All(ctx)
	if err != nil {
		h.log.Error("list warehouses failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list warehouses")
		return
	}

	resp := make([]warehouseResponse, len(warehouses))
	for i, wh := range warehouses {
		resp[i] = warehouseToResponse(wh)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetWarehouse handles GET /{tenant}/inventory/warehouses/{warehouseID}.
func (h *WarehouseHandler) GetWarehouse(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	warehouseIDStr := chi.URLParam(r, "warehouseID")
	warehouseID, err := uuid.Parse(warehouseIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warehouse ID")
		return
	}

	wh, err := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(warehouseID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Warehouse not found")
		return
	}

	writeJSON(w, http.StatusOK, warehouseToResponse(wh))
}

// CreateWarehouse handles POST /{tenant}/inventory/warehouses.
func (h *WarehouseHandler) CreateWarehouse(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req warehouseDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "name and code are required")
		return
	}

	q := h.orm.Warehouse.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(req.Code).
		SetAddress(req.Address).
		SetIsDefault(req.IsDefault)

	if req.Latitude != nil {
		q = q.SetLatitude(*req.Latitude)
	}
	if req.Longitude != nil {
		q = q.SetLongitude(*req.Longitude)
	}
	if req.OutletID != nil {
		q = q.SetOutletID(*req.OutletID)
	}
	if req.IsActive != nil {
		q = q.SetIsActive(*req.IsActive)
	}

	wh, err := q.Save(r.Context())
	if err != nil {
		h.log.Error("create warehouse failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create warehouse")
		return
	}

	writeJSON(w, http.StatusCreated, warehouseToResponse(wh))
}

// UpdateWarehouse handles PUT /{tenant}/inventory/warehouses/{warehouseID}.
func (h *WarehouseHandler) UpdateWarehouse(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	warehouseIDStr := chi.URLParam(r, "warehouseID")
	warehouseID, err := uuid.Parse(warehouseIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warehouse ID")
		return
	}

	var req warehouseDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	ctx := r.Context()

	// Verify ownership.
	_, err = h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(warehouseID)).
		Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Warehouse not found")
		return
	}

	upd := h.orm.Warehouse.UpdateOneID(warehouseID).
		SetName(req.Name).
		SetCode(req.Code).
		SetAddress(req.Address).
		SetIsDefault(req.IsDefault)

	if req.Latitude != nil {
		upd = upd.SetLatitude(*req.Latitude)
	}
	if req.Longitude != nil {
		upd = upd.SetLongitude(*req.Longitude)
	}
	if req.OutletID != nil {
		upd = upd.SetOutletID(*req.OutletID)
	}
	if req.IsActive != nil {
		upd = upd.SetIsActive(*req.IsActive)
	}

	wh, err := upd.Save(ctx)
	if err != nil {
		h.log.Error("update warehouse failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update warehouse")
		return
	}

	writeJSON(w, http.StatusOK, warehouseToResponse(wh))
}

// DeleteWarehouse handles DELETE /{tenant}/inventory/warehouses/{warehouseID}.
func (h *WarehouseHandler) DeleteWarehouse(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	warehouseIDStr := chi.URLParam(r, "warehouseID")
	warehouseID, err := uuid.Parse(warehouseIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warehouse ID")
		return
	}

	ctx := r.Context()

	_, err = h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(warehouseID)).
		Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Warehouse not found")
		return
	}

	if err := h.orm.Warehouse.DeleteOneID(warehouseID).Exec(ctx); err != nil {
		h.log.Error("delete warehouse failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete warehouse")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
