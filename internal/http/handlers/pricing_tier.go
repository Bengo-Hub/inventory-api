package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entip "github.com/bengobox/inventory-service/internal/ent/itempricing"
	entpt "github.com/bengobox/inventory-service/internal/ent/pricingtier"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

type PricingTierHandler struct {
	log     *zap.Logger
	orm     *ent.Client
	rbacSvc *rbac.Service
}

func NewPricingTierHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *PricingTierHandler {
	return &PricingTierHandler{
		log:     log.Named("pricing_tier.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

func (h *PricingTierHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory/pricing-tiers", func(pt chi.Router) {
		pt.Get("/", h.ListTiers)
		pt.With(perm(rbac.PermItemsAdd)).Post("/", h.CreateTier)
		pt.With(perm(rbac.PermItemsChange)).Put("/{tierID}", h.UpdateTier)
		pt.With(perm(rbac.PermItemsDelete)).Delete("/{tierID}", h.DeactivateTier)
	})

	r.Route("/inventory/items/{itemID}/pricing", func(ip chi.Router) {
		ip.Get("/", h.GetItemPricing)
		ip.With(perm(rbac.PermItemsChange)).Put("/", h.UpsertItemPricing)
	})
}

// --- PricingTier DTOs ---

type pricingTierDTO struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Description string    `json:"description,omitempty"`
	IsDefault   bool      `json:"is_default"`
	IsActive    bool      `json:"is_active"`
	SortOrder   int       `json:"sort_order"`
}

type createTierReq struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default"`
	SortOrder   int    `json:"sort_order"`
}

type updateTierReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsDefault   *bool  `json:"is_default"`
	IsActive    *bool  `json:"is_active"`
	SortOrder   *int   `json:"sort_order"`
}

// --- ItemPricing DTOs ---

type itemPricingDTO struct {
	ID            uuid.UUID  `json:"id"`
	ItemID        uuid.UUID  `json:"item_id"`
	PricingTierID uuid.UUID  `json:"pricing_tier_id"`
	TierName      string     `json:"tier_name,omitempty"`
	TierCode      string     `json:"tier_code,omitempty"`
	Price         float64    `json:"price"`
	Currency      string     `json:"currency"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	IsActive      bool       `json:"is_active"`
}

type upsertItemPricingEntry struct {
	PricingTierID uuid.UUID  `json:"pricing_tier_id"`
	Price         float64    `json:"price"`
	Currency      string     `json:"currency"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
}

// --- PricingTier handlers ---

func (h *PricingTierHandler) ListTiers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	tiers, err := h.orm.PricingTier.Query().
		Where(entpt.TenantID(tenantID)).
		Order(ent.Asc(entpt.FieldSortOrder), ent.Asc(entpt.FieldName)).
		All(r.Context())
	if err != nil {
		h.log.Error("list pricing tiers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list pricing tiers")
		return
	}

	dtos := make([]pricingTierDTO, 0, len(tiers))
	for _, t := range tiers {
		dtos = append(dtos, toPricingTierDTO(t))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (h *PricingTierHandler) CreateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req createTierReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "name and code are required")
		return
	}

	tier, err := h.orm.PricingTier.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(req.Code).
		SetDescription(req.Description).
		SetIsDefault(req.IsDefault).
		SetSortOrder(req.SortOrder).
		Save(r.Context())
	if err != nil {
		h.log.Error("create pricing tier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create pricing tier")
		return
	}
	writeJSON(w, http.StatusCreated, toPricingTierDTO(tier))
}

func (h *PricingTierHandler) UpdateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	tierID, err := uuid.Parse(chi.URLParam(r, "tierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TIER", "Invalid tier ID")
		return
	}

	existing, err := h.orm.PricingTier.Get(r.Context(), tierID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Pricing tier not found")
		return
	}

	var req updateTierReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	upd := h.orm.PricingTier.UpdateOneID(tierID)
	if req.Name != "" {
		upd = upd.SetName(req.Name)
	}
	if req.Description != "" {
		upd = upd.SetDescription(req.Description)
	}
	if req.IsDefault != nil {
		upd = upd.SetIsDefault(*req.IsDefault)
	}
	if req.IsActive != nil {
		upd = upd.SetIsActive(*req.IsActive)
	}
	if req.SortOrder != nil {
		upd = upd.SetSortOrder(*req.SortOrder)
	}

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update pricing tier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update pricing tier")
		return
	}
	writeJSON(w, http.StatusOK, toPricingTierDTO(updated))
}

func (h *PricingTierHandler) DeactivateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	tierID, err := uuid.Parse(chi.URLParam(r, "tierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TIER", "Invalid tier ID")
		return
	}

	existing, err := h.orm.PricingTier.Get(r.Context(), tierID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Pricing tier not found")
		return
	}

	if _, err := h.orm.PricingTier.UpdateOneID(tierID).SetIsActive(false).Save(r.Context()); err != nil {
		h.log.Error("deactivate pricing tier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DEACTIVATE_FAILED", "Failed to deactivate tier")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}

// --- ItemPricing handlers ---

func (h *PricingTierHandler) GetItemPricing(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM", "Invalid item ID")
		return
	}

	pricings, err := h.orm.ItemPricing.Query().
		Where(entip.TenantID(tenantID), entip.ItemID(itemID), entip.IsActive(true)).
		All(r.Context())
	if err != nil {
		h.log.Error("get item pricing failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to get item pricing")
		return
	}

	dtos := make([]itemPricingDTO, 0, len(pricings))
	for _, p := range pricings {
		dtos = append(dtos, toItemPricingDTO(p))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (h *PricingTierHandler) UpsertItemPricing(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM", "Invalid item ID")
		return
	}

	var entries []upsertItemPricingEntry
	if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Expected array of pricing entries")
		return
	}

	results := make([]itemPricingDTO, 0, len(entries))
	for _, entry := range entries {
		existing, err := h.orm.ItemPricing.Query().
			Where(entip.TenantID(tenantID), entip.ItemID(itemID), entip.PricingTierID(entry.PricingTierID)).
			First(r.Context())

		effectiveFrom := time.Now()
		if entry.EffectiveFrom != nil {
			effectiveFrom = *entry.EffectiveFrom
		}
		currency := entry.Currency
		if currency == "" {
			currency = "KES"
		}

		var saved *ent.ItemPricing
		if err == nil {
			// update existing
			upd := h.orm.ItemPricing.UpdateOneID(existing.ID).
				SetPrice(entry.Price).
				SetCurrency(currency).
				SetEffectiveFrom(effectiveFrom).
				SetIsActive(true)
			if entry.EffectiveTo != nil {
				upd = upd.SetEffectiveTo(*entry.EffectiveTo)
			} else {
				upd = upd.ClearEffectiveTo()
			}
			saved, err = upd.Save(r.Context())
		} else {
			// create new
			creator := h.orm.ItemPricing.Create().
				SetTenantID(tenantID).
				SetItemID(itemID).
				SetPricingTierID(entry.PricingTierID).
				SetPrice(entry.Price).
				SetCurrency(currency).
				SetEffectiveFrom(effectiveFrom)
			if entry.EffectiveTo != nil {
				creator = creator.SetEffectiveTo(*entry.EffectiveTo)
			}
			saved, err = creator.Save(r.Context())
		}
		if err != nil {
			h.log.Error("upsert item pricing failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "UPSERT_FAILED", "Failed to upsert item pricing")
			return
		}
		results = append(results, toItemPricingDTO(saved))
	}

	writeJSON(w, http.StatusOK, results)
}

func toPricingTierDTO(t *ent.PricingTier) pricingTierDTO {
	return pricingTierDTO{
		ID:          t.ID,
		TenantID:    t.TenantID,
		Name:        t.Name,
		Code:        t.Code,
		Description: t.Description,
		IsDefault:   t.IsDefault,
		IsActive:    t.IsActive,
		SortOrder:   t.SortOrder,
	}
}

func toItemPricingDTO(p *ent.ItemPricing) itemPricingDTO {
	dto := itemPricingDTO{
		ID:            p.ID,
		ItemID:        p.ItemID,
		PricingTierID: p.PricingTierID,
		Price:         p.Price,
		Currency:      p.Currency,
		EffectiveFrom: p.EffectiveFrom,
		IsActive:      p.IsActive,
	}
	if p.EffectiveTo != nil {
		dto.EffectiveTo = p.EffectiveTo
	}
	return dto
}
