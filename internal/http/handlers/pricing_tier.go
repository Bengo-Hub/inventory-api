package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
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
		// Bulk-generate every item's price for this tier from the default tier (× factor) or
		// from cost + margin — so a Wholesale tier can be populated in one click instead of
		// item-by-item. Generated prices are clamped to each item's min/max band.
		pt.With(perm(rbac.PermItemsChange)).Post("/{tierID}/generate", h.GenerateTierPricing)
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

	// Load the item's selling-price guardrails once and reject any out-of-band tier price.
	// Hard floor/ceiling — keeps catalog/POS prices coherent with the item's configured band.
	itm, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.ID(itemID)).
		Select(entitem.FieldMinSellingPrice, entitem.FieldMaxSellingPrice).
		First(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "ITEM_NOT_FOUND", "Item not found")
		return
	}
	for _, entry := range entries {
		if itm.MinSellingPrice != nil && entry.Price < *itm.MinSellingPrice {
			writeError(w, http.StatusUnprocessableEntity, "PRICE_BELOW_MIN",
				fmt.Sprintf("price %.2f is below the item minimum %.2f", entry.Price, *itm.MinSellingPrice))
			return
		}
		if itm.MaxSellingPrice != nil && entry.Price > *itm.MaxSellingPrice {
			writeError(w, http.StatusUnprocessableEntity, "PRICE_ABOVE_MAX",
				fmt.Sprintf("price %.2f is above the item maximum %.2f", entry.Price, *itm.MaxSellingPrice))
			return
		}
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

type generateTierPricingReq struct {
	// Source basis: "default_tier" (price = default tier price × factor) or
	// "cost_margin" (price = cost_price / (1 - margin_percent/100)).
	Source        string  `json:"source"`
	Factor        float64 `json:"factor"`         // multiplier for default_tier (e.g. 0.9 = 10% off)
	MarginPercent float64 `json:"margin_percent"` // margin for cost_margin (0 < m < 100)
	Overwrite     bool    `json:"overwrite"`      // replace existing prices on this tier
}

type generateTierPricingResp struct {
	Generated int      `json:"generated"`
	Skipped   int      `json:"skipped"`
	Clamped   int      `json:"clamped"`
	Warnings  []string `json:"warnings,omitempty"`
}

// GenerateTierPricing bulk-populates a pricing tier's per-item prices.
// POST /inventory/pricing-tiers/{tierID}/generate
func (h *PricingTierHandler) GenerateTierPricing(w http.ResponseWriter, r *http.Request) {
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
	var req generateTierPricingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Source == "" {
		req.Source = "default_tier"
	}
	if req.Source == "default_tier" && req.Factor <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_FACTOR", "factor must be > 0 for default_tier source")
		return
	}
	if req.Source == "cost_margin" && (req.MarginPercent <= 0 || req.MarginPercent >= 100) {
		writeError(w, http.StatusBadRequest, "INVALID_MARGIN", "margin_percent must be between 0 and 100")
		return
	}

	// Validate the target tier belongs to the tenant and is active.
	tier, err := h.orm.PricingTier.Query().Where(entpt.ID(tierID), entpt.TenantID(tenantID), entpt.IsActive(true)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "TIER_NOT_FOUND", "Pricing tier not found")
		return
	}

	// Default-tier price per item (source=default_tier) — built from active default-tier pricings.
	defaultPrices := map[uuid.UUID]float64{}
	if req.Source == "default_tier" {
		defTier, derr := h.orm.PricingTier.Query().Where(entpt.TenantID(tenantID), entpt.IsActive(true), entpt.IsDefault(true)).First(r.Context())
		if derr != nil {
			writeError(w, http.StatusUnprocessableEntity, "NO_DEFAULT_TIER", "No default pricing tier to derive from")
			return
		}
		dps, _ := h.orm.ItemPricing.Query().
			Where(entip.TenantID(tenantID), entip.IsActive(true), entip.PricingTierID(defTier.ID), entip.OutletIDIsNil()).
			All(r.Context())
		for _, p := range dps {
			defaultPrices[p.ItemID] = p.Price
		}
	}

	// Existing target-tier prices (to honor overwrite=false).
	existing := map[uuid.UUID]bool{}
	{
		eps, _ := h.orm.ItemPricing.Query().
			Where(entip.TenantID(tenantID), entip.PricingTierID(tierID), entip.OutletIDIsNil()).
			All(r.Context())
		for _, p := range eps {
			existing[p.ItemID] = true
		}
	}

	items, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IsActive(true)).
		Select(entitem.FieldID, entitem.FieldCostPrice, entitem.FieldMinSellingPrice, entitem.FieldMaxSellingPrice).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to load items")
		return
	}

	resp := generateTierPricingResp{}
	for _, it := range items {
		if existing[it.ID] && !req.Overwrite {
			resp.Skipped++
			continue
		}
		var base float64
		switch req.Source {
		case "default_tier":
			base = defaultPrices[it.ID]
			if base <= 0 {
				resp.Skipped++
				continue
			}
			base = base * req.Factor
		case "cost_margin":
			if it.CostPrice == nil || *it.CostPrice <= 0 {
				resp.Skipped++
				continue
			}
			base = *it.CostPrice / (1 - req.MarginPercent/100)
		default:
			writeError(w, http.StatusBadRequest, "INVALID_SOURCE", "source must be default_tier or cost_margin")
			return
		}
		price := math.Ceil(base)
		// Clamp into the item's configured selling-price band.
		if it.MinSellingPrice != nil && price < *it.MinSellingPrice {
			price = *it.MinSellingPrice
			resp.Clamped++
		}
		if it.MaxSellingPrice != nil && price > *it.MaxSellingPrice {
			price = *it.MaxSellingPrice
			resp.Clamped++
		}
		saved, uerr := h.upsertTierPrice(r.Context(), tenantID, it.ID, tierID, price)
		if uerr != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("item %s: %v", it.ID, uerr))
			continue
		}
		resp.Generated++
		go h.publishPricingUpdatedEvent(context.Background(), tenantID, it.ID, saved)
	}
	h.log.Info("generated tier pricing", zap.String("tier", tier.Code), zap.Int("generated", resp.Generated), zap.Int("skipped", resp.Skipped))
	writeJSON(w, http.StatusOK, resp)
}

// upsertTierPrice creates or updates the (tenant,item,tier) all-outlets pricing row at the given price.
func (h *PricingTierHandler) upsertTierPrice(ctx context.Context, tenantID, itemID, tierID uuid.UUID, price float64) (*ent.ItemPricing, error) {
	existing, err := h.orm.ItemPricing.Query().
		Where(entip.TenantID(tenantID), entip.ItemID(itemID), entip.PricingTierID(tierID), entip.OutletIDIsNil()).
		First(ctx)
	if err == nil {
		return h.orm.ItemPricing.UpdateOneID(existing.ID).SetPrice(price).SetIsActive(true).SetEffectiveFrom(time.Now()).Save(ctx)
	}
	return h.orm.ItemPricing.Create().
		SetTenantID(tenantID).SetItemID(itemID).SetPricingTierID(tierID).
		SetPrice(price).SetCurrency("KES").SetEffectiveFrom(time.Now()).Save(ctx)
}

// publishPricingUpdatedEvent writes an inventory.item.pricing_updated outbox event.
func (h *PricingTierHandler) publishPricingUpdatedEvent(ctx context.Context, tenantID, itemID uuid.UUID, p *ent.ItemPricing) {
	evt := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "inventory",
		AggregateID:   itemID,
		EventType:     "item.pricing_updated",
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
