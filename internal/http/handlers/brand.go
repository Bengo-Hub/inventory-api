package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entib "github.com/bengobox/inventory-service/internal/ent/itembrand"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// BrandHandler exposes tenant-scoped CRUD for item brands.
// Mirrors PricingTierHandler / ItemCategory so the POS/ordering UIs can show a Brands tab.
type BrandHandler struct {
	log     *zap.Logger
	orm     *ent.Client
	rbacSvc *rbac.Service
}

func NewBrandHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *BrandHandler {
	return &BrandHandler{
		log:     log.Named("brand.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

func (h *BrandHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory/brands", func(b chi.Router) {
		b.Get("/", h.ListBrands)
		b.With(perm(rbac.PermItemsAdd)).Post("/", h.CreateBrand)
		b.With(perm(rbac.PermItemsChange)).Put("/{brandID}", h.UpdateBrand)
		b.With(perm(rbac.PermItemsDelete)).Delete("/{brandID}", h.DeleteBrand)
	})
}

// --- Brand DTOs ---

type brandDTO struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Description string    `json:"description,omitempty"`
	LogoURL     string    `json:"logo_url,omitempty"`
	IsActive    bool      `json:"is_active"`
	SortOrder   int       `json:"sort_order"`
}

type createBrandReq struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	LogoURL     string `json:"logo_url"`
	SortOrder   int    `json:"sort_order"`
}

type updateBrandReq struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	LogoURL     string `json:"logo_url"`
	IsActive    *bool  `json:"is_active"`
	SortOrder   *int   `json:"sort_order"`
}

// --- Brand handlers ---

// ListBrands handles GET /inventory/brands — returns active brands sorted by sort_order, name.
func (h *BrandHandler) ListBrands(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	brands, err := h.orm.ItemBrand.Query().
		Where(entib.TenantID(tenantID), entib.IsActive(true)).
		Order(ent.Asc(entib.FieldSortOrder), ent.Asc(entib.FieldName)).
		All(r.Context())
	if err != nil {
		h.log.Error("list brands failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list brands")
		return
	}

	dtos := make([]brandDTO, 0, len(brands))
	for _, b := range brands {
		dtos = append(dtos, toBrandDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  dtos,
		"total": len(dtos),
	})
}

// CreateBrand handles POST /inventory/brands.
func (h *BrandHandler) CreateBrand(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req createBrandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	code := req.Code
	if code == "" {
		code = slugify(req.Name)
	}

	brand, err := h.orm.ItemBrand.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(code).
		SetDescription(req.Description).
		SetLogoURL(req.LogoURL).
		SetSortOrder(req.SortOrder).
		Save(r.Context())
	if err != nil {
		h.log.Error("create brand failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create brand")
		return
	}
	writeJSON(w, http.StatusCreated, toBrandDTO(brand))
}

// UpdateBrand handles PUT /inventory/brands/{brandID}.
func (h *BrandHandler) UpdateBrand(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	brandID, err := uuid.Parse(chi.URLParam(r, "brandID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BRAND", "Invalid brand ID")
		return
	}

	existing, err := h.orm.ItemBrand.Get(r.Context(), brandID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Brand not found")
		return
	}

	var req updateBrandReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	upd := h.orm.ItemBrand.UpdateOneID(brandID)
	if req.Name != "" {
		upd = upd.SetName(req.Name)
	}
	if req.Code != "" {
		upd = upd.SetCode(req.Code)
	}
	if req.Description != "" {
		upd = upd.SetDescription(req.Description)
	}
	if req.LogoURL != "" {
		upd = upd.SetLogoURL(req.LogoURL)
	}
	if req.IsActive != nil {
		upd = upd.SetIsActive(*req.IsActive)
	}
	if req.SortOrder != nil {
		upd = upd.SetSortOrder(*req.SortOrder)
	}

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update brand failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update brand")
		return
	}
	writeJSON(w, http.StatusOK, toBrandDTO(updated))
}

// DeleteBrand handles DELETE /inventory/brands/{brandID} — soft-deletes (deactivates) a brand.
func (h *BrandHandler) DeleteBrand(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	brandID, err := uuid.Parse(chi.URLParam(r, "brandID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BRAND", "Invalid brand ID")
		return
	}

	existing, err := h.orm.ItemBrand.Get(r.Context(), brandID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Brand not found")
		return
	}

	if _, err := h.orm.ItemBrand.UpdateOneID(brandID).SetIsActive(false).Save(r.Context()); err != nil {
		h.log.Error("delete brand failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete brand")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// slugify produces a URL-safe brand code from a name (e.g. "Coca-Cola" -> "coca-cola").
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func toBrandDTO(b *ent.ItemBrand) brandDTO {
	return brandDTO{
		ID:          b.ID,
		TenantID:    b.TenantID,
		Name:        b.Name,
		Code:        b.Code,
		Description: b.Description,
		LogoURL:     b.LogoURL,
		IsActive:    b.IsActive,
		SortOrder:   b.SortOrder,
	}
}
