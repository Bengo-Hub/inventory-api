package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
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

	// Quantity-aware price resolution: returns unit price + total for N units using default tier.
	r.Get("/inventory/items/{itemID}/price", h.GetItemPrice)

	// Bulk endpoint: returns default-tier price for every item in the tenant.
	// Used by downstream services (pos-api, etc.) to show prices without N+1 calls.
	r.Get("/inventory/items/pricing", h.ListAllItemPricing)
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
	OutletID      *uuid.UUID `json:"outlet_id,omitempty"`
	TierBasis     string     `json:"tier_basis,omitempty"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	IsActive      bool       `json:"is_active"`
}

type upsertItemPricingEntry struct {
	PricingTierID uuid.UUID  `json:"pricing_tier_id"`
	Price         float64    `json:"price"`
	Currency      string     `json:"currency"`
	OutletID      *uuid.UUID `json:"outlet_id"`
	TierBasis     string     `json:"tier_basis"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
}

// --- PricingTier handlers ---

// defaultPricingTiers are seeded the first time a tenant's tiers are listed, so the pricing-profile
// feature (Retail/Wholesale prices at the POS) works out of the box. Per-tier item prices are then
// configured by admins via the item-pricing endpoint.
var defaultPricingTiers = []struct {
	Name      string
	Code      string
	IsDefault bool
	SortOrder int
}{
	{"Retail", "RETAIL", true, 0},
	{"Wholesale", "WHOLESALE", false, 1},
}

// ensureDefaultTiers creates the default Retail/Wholesale tiers for a tenant that has none yet.
// Concurrency-safe: the (tenant_id, code) unique index drops duplicate inserts.
func (h *PricingTierHandler) ensureDefaultTiers(ctx context.Context, tenantID uuid.UUID) {
	if exists, err := h.orm.PricingTier.Query().Where(entpt.TenantID(tenantID)).Exist(ctx); err != nil || exists {
		return
	}
	for _, t := range defaultPricingTiers {
		if _, err := h.orm.PricingTier.Create().
			SetTenantID(tenantID).
			SetName(t.Name).
			SetCode(t.Code).
			SetIsDefault(t.IsDefault).
			SetIsActive(true).
			SetSortOrder(t.SortOrder).
			Save(ctx); err != nil {
			h.log.Warn("ensureDefaultTiers: seed failed", zap.String("code", t.Code), zap.Error(err))
		}
	}
}

func (h *PricingTierHandler) ListTiers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	h.ensureDefaultTiers(r.Context(), tenantID)

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
		q := h.orm.ItemPricing.Query().
			Where(entip.TenantID(tenantID), entip.ItemID(itemID), entip.PricingTierID(entry.PricingTierID))
		if entry.OutletID != nil {
			q = q.Where(entip.OutletID(*entry.OutletID))
		} else {
			q = q.Where(entip.OutletIDIsNil())
		}
		existing, err := q.First(r.Context())

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
			if entry.TierBasis != "" {
				upd = upd.SetTierBasis(entip.TierBasis(entry.TierBasis))
			}
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
			if entry.OutletID != nil {
				creator = creator.SetOutletID(*entry.OutletID)
			}
			if entry.TierBasis != "" {
				creator = creator.SetTierBasis(entip.TierBasis(entry.TierBasis))
			}
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

		// Publish pricing_updated event to outbox (best-effort, non-blocking).
		go h.publishPricingUpdatedEvent(r.Context(), tenantID, itemID, saved)
	}

	writeJSON(w, http.StatusOK, results)
}

// publishPricingUpdatedEvent writes an inventory.item.pricing_updated outbox event.
func (h *PricingTierHandler) publishPricingUpdatedEvent(ctx context.Context, tenantID, itemID uuid.UUID, p *ent.ItemPricing) {
	evt := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item_pricing",
		AggregateID:   itemID,
		EventType:     "inventory.item.pricing_updated",
		Payload: map[string]any{
			"item_id":         itemID,
			"pricing_tier_id": p.PricingTierID,
			"price":           p.Price,
			"currency":        p.Currency,
			"effective_from":  p.EffectiveFrom,
			"is_active":       p.IsActive,
			"updated_at":      p.UpdatedAt,
		},
		Timestamp: time.Now().UTC(),
	}
	payload, err := evt.ToJSON()
	if err != nil {
		h.log.Warn("pricing_updated event: marshal failed", zap.Error(err))
		return
	}
	_, err = h.orm.OutboxEvent.Create().
		SetID(evt.ID).
		SetTenantID(tenantID).
		SetAggregateType(evt.AggregateType).
		SetAggregateID(evt.AggregateID.String()).
		SetEventType(evt.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(evt.Timestamp).
		Save(ctx)
	if err != nil {
		h.log.Warn("pricing_updated event: outbox write failed", zap.Error(err))
	}
}

// bulkItemPriceDTO is a flattened price entry used by downstream services (e.g. pos-api).
// `Price`/`TierCode` carry the DEFAULT tier (back-compat); `Prices` carries EVERY active tier's
// price keyed by tier code (e.g. {"RETAIL":320,"WHOLESALE":290}) so the POS terminal can switch
// pricing profiles client-side without a per-item round-trip.
type bulkItemPriceDTO struct {
	ItemID   uuid.UUID          `json:"item_id"`
	Price    float64            `json:"price"`
	Currency string             `json:"currency"`
	TierCode string             `json:"tier_code"`
	Prices   map[string]float64 `json:"prices"`
}

// ListAllItemPricing returns the default-tier price for every item in the tenant, plus the full
// per-tier price map for each item. Falls back to the first active pricing entry when no default
// tier exists.
// GET /v1/{slug}/inventory/items/pricing
func (h *PricingTierHandler) ListAllItemPricing(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	// Load pricing tiers to identify the default tier.
	tiers, err := h.orm.PricingTier.Query().
		Where(entpt.TenantID(tenantID), entpt.IsActive(true)).
		All(r.Context())
	if err != nil {
		h.log.Error("list pricing tiers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list pricing tiers")
		return
	}
	tierMeta := make(map[uuid.UUID]struct {
		code      string
		isDefault bool
	}, len(tiers))
	for _, t := range tiers {
		tierMeta[t.ID] = struct {
			code      string
			isDefault bool
		}{t.Code, t.IsDefault}
	}

	// Load all active pricing entries for the tenant in one query.
	pricings, err := h.orm.ItemPricing.Query().
		Where(entip.TenantID(tenantID), entip.IsActive(true)).
		All(r.Context())
	if err != nil {
		h.log.Error("list all item pricing failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list item pricing")
		return
	}

	// For each item keep the default-tier entry (fall back to first found) AND accumulate every
	// tier's price into a per-item code→price map.
	type entry struct {
		price     float64
		currency  string
		tierCode  string
		isDefault bool
		prices    map[string]float64
	}
	best := make(map[uuid.UUID]entry, len(pricings))
	for _, p := range pricings {
		meta := tierMeta[p.PricingTierID]
		prev, exists := best[p.ItemID]
		if !exists {
			prev = entry{prices: map[string]float64{}}
		}
		// Record this tier's price in the per-item map (skip blank codes).
		if meta.code != "" {
			prev.prices[meta.code] = p.Price
		}
		// Promote to the default-tier entry when this is the default (or the first seen).
		if !exists || (!prev.isDefault && meta.isDefault) {
			prev.price = p.Price
			prev.currency = p.Currency
			prev.tierCode = meta.code
			prev.isDefault = meta.isDefault
		}
		best[p.ItemID] = prev
	}

	out := make([]bulkItemPriceDTO, 0, len(best))
	for itemID, e := range best {
		out = append(out, bulkItemPriceDTO{
			ItemID:   itemID,
			Price:    e.price,
			Currency: e.currency,
			TierCode: e.tierCode,
			Prices:   e.prices,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// itemPriceDTO is the response for the quantity-aware price resolution endpoint.
type itemPriceDTO struct {
	ItemID     uuid.UUID `json:"item_id"`
	TierID     uuid.UUID `json:"tier_id"`
	TierName   string    `json:"tier_name"`
	TierCode   string    `json:"tier_code"`
	UnitPrice  float64   `json:"unit_price"`
	Currency   string    `json:"currency"`
	Quantity   int       `json:"quantity"`
	TotalPrice float64   `json:"total_price"`
}

// GetItemPrice resolves the effective price for an item at a given quantity.
// Uses the default pricing tier; falls back to the first active tier if no default exists.
// GET /v1/{slug}/inventory/items/{itemID}/price?quantity=N
func (h *PricingTierHandler) GetItemPrice(w http.ResponseWriter, r *http.Request) {
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

	quantity := 1
	if qStr := r.URL.Query().Get("quantity"); qStr != "" {
		if q, parseErr := strconv.Atoi(qStr); parseErr == nil && q > 0 {
			quantity = q
		}
	}
	// Optional explicit pricing tier (e.g. RETAIL, WHOLESALE); empty = default tier.
	tierCode := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("tier")))

	ctx := r.Context()

	// Load active pricing tiers to identify the default.
	tiers, err := h.orm.PricingTier.Query().
		Where(entpt.TenantID(tenantID), entpt.IsActive(true)).
		All(ctx)
	if err != nil {
		h.log.Error("GetItemPrice: load tiers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load pricing tiers")
		return
	}

	tierMeta := make(map[uuid.UUID]*ent.PricingTier, len(tiers))
	for _, t := range tiers {
		tierMeta[t.ID] = t
	}

	// Load all active pricing entries for this item.
	pricings, err := h.orm.ItemPricing.Query().
		Where(entip.TenantID(tenantID), entip.ItemID(itemID), entip.IsActive(true)).
		All(ctx)
	if err != nil {
		h.log.Error("GetItemPrice: load pricings failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load item pricing")
		return
	}

	if len(pricings) == 0 {
		writeError(w, http.StatusNotFound, "NO_PRICING", "No active pricing found for this item")
		return
	}

	var chosen *ent.ItemPricing
	var chosenTier *ent.PricingTier
	// Explicit tier requested → exact (case-insensitive) code match.
	if tierCode != "" {
		for _, p := range pricings {
			if t := tierMeta[p.PricingTierID]; t != nil && strings.EqualFold(t.Code, tierCode) {
				chosen, chosenTier = p, t
				break
			}
		}
	}
	// Otherwise (or no match) prefer the default tier, then the first active entry.
	if chosen == nil {
		for _, p := range pricings {
			t := tierMeta[p.PricingTierID]
			if chosen == nil {
				chosen, chosenTier = p, t
				continue
			}
			if t != nil && t.IsDefault && (chosenTier == nil || !chosenTier.IsDefault) {
				chosen, chosenTier = p, t
			}
		}
	}

	dto := itemPriceDTO{
		ItemID:     itemID,
		UnitPrice:  chosen.Price,
		Currency:   chosen.Currency,
		Quantity:   quantity,
		TotalPrice: chosen.Price * float64(quantity),
	}
	if chosenTier != nil {
		dto.TierID = chosenTier.ID
		dto.TierName = chosenTier.Name
		dto.TierCode = chosenTier.Code
	} else {
		dto.TierID = chosen.PricingTierID
	}

	writeJSON(w, http.StatusOK, dto)
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
		OutletID:      p.OutletID,
		TierBasis:     string(p.TierBasis),
		EffectiveFrom: p.EffectiveFrom,
		IsActive:      p.IsActive,
	}
	if p.EffectiveTo != nil {
		dto.EffectiveTo = p.EffectiveTo
	}
	return dto
}
