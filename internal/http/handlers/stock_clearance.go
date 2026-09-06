package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// requireBatchPeriodPricing gates a route on the "batch_period_pricing" add-on (platform-admin
// TenantFeatureGrant, see subscriptions-api) — mirrors inventory_settings.go's wantsLocked check
// for the settings toggle itself, but enforced here too so the report/actions aren't reachable
// via direct API call even if a client never fetched settings first.
func requireBatchPeriodPricing(r *http.Request) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	return ok && claims != nil && claims.FeatureEnabled("batch_period_pricing")
}

// StockClearanceHandler exposes the Aging Stock report and the Start/Cancel Clearance actions
// (2026-09-06 pricing/tiering plan, Phase 2).
type StockClearanceHandler struct {
	log      *zap.Logger
	itemsSvc *items.Service
	rbacSvc  *rbac.Service
	settings *InventorySettingsHandler
}

func NewStockClearanceHandler(log *zap.Logger, itemsSvc *items.Service, settings *InventorySettingsHandler) *StockClearanceHandler {
	return &StockClearanceHandler{log: log.Named("stock_clearance.handler"), itemsSvc: itemsSvc, settings: settings}
}

func (h *StockClearanceHandler) SetRBACService(svc *rbac.Service) { h.rbacSvc = svc }

func (h *StockClearanceHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}
	r.Get("/inventory/aging-stock", h.AgingStock)
	r.Route("/inventory/items/{itemID}/clearance", func(cr chi.Router) {
		cr.With(perm(rbac.PermItemsChange)).Post("/", h.StartClearance)
		cr.With(perm(rbac.PermItemsChange)).Delete("/", h.CancelClearance)
	})
}

// AgingStock handles GET /{tenant}/inventory/aging-stock — items whose oldest active lot is
// older than the tenant's configured threshold and not already under an active clearance.
func (h *StockClearanceHandler) AgingStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}
	if !requireBatchPeriodPricing(r) {
		authclient.WriteFeatureLocked(w, "batch_period_pricing", "")
		return
	}
	ctx := r.Context()

	threshold := 90
	if h.settings != nil {
		if cfg, cErr := h.settings.getOrCreate(r, tenantID); cErr == nil && cfg.StockAgingThresholdDays > 0 {
			threshold = cfg.StockAgingThresholdDays
		}
	}
	if v := r.URL.Query().Get("threshold_days"); v != "" {
		if n, pErr := strconv.Atoi(v); pErr == nil && n > 0 {
			threshold = n
		}
	}

	rows, err := h.itemsSvc.AgingStockReport(ctx, tenantID, threshold)
	if err != nil {
		h.log.Error("aging stock report failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query_failed", "failed to load aging stock report")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threshold_days": threshold, "items": rows})
}

type startClearanceInput struct {
	MarkdownPrice   float64    `json:"markdown_price"`
	ReferenceBefore *time.Time `json:"reference_before"`
	EndsAt          *time.Time `json:"ends_at"`
	Notes           string     `json:"notes"`
}

// StartClearance handles POST /{tenant}/inventory/items/{itemID}/clearance. Gated the same way
// as the settings toggle that must be on before this is reachable in practice (per-tenant
// enablement lives in TenantInventoryConfig.batch_period_pricing_enabled, itself gated on the
// platform-admin "batch_period_pricing" grant) -- this endpoint itself only checks the standard
// inventory.items.change permission, matching every other price-mutating route in this package.
func (h *StockClearanceHandler) StartClearance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_item", "invalid item ID")
		return
	}
	if !requireBatchPeriodPricing(r) {
		authclient.WriteFeatureLocked(w, "batch_period_pricing", "")
		return
	}
	var input startClearanceInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}
	if input.MarkdownPrice <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_price", "markdown_price must be positive")
		return
	}
	referenceBefore := time.Now()
	if input.ReferenceBefore != nil {
		referenceBefore = *input.ReferenceBefore
	}

	actor := actorFromRequest(r)
	saved, err := h.itemsSvc.StartClearance(r.Context(), tenantID, itemID, input.MarkdownPrice, referenceBefore, input.EndsAt, actor, input.Notes)
	if err != nil {
		h.log.Error("start clearance failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "start_failed", "failed to start clearance")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// CancelClearance handles DELETE /{tenant}/inventory/items/{itemID}/clearance.
func (h *StockClearanceHandler) CancelClearance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_item", "invalid item ID")
		return
	}
	if !requireBatchPeriodPricing(r) {
		authclient.WriteFeatureLocked(w, "batch_period_pricing", "")
		return
	}
	actor := actorFromRequest(r)
	if err := h.itemsSvc.CancelClearance(r.Context(), tenantID, itemID, actor); err != nil {
		h.log.Error("cancel clearance failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "cancel_failed", "failed to cancel clearance")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
