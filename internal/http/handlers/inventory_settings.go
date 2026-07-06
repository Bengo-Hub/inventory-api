package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

// DefaultUnitReorderLevels returns sensible per-unit reorder minimums used when
// an item has no explicit reorder level configured and the tenant has no custom
// unit defaults saved. Values are in the natural unit (e.g. g for GRAM, ml for ML).
func DefaultUnitReorderLevels() map[string]int {
	return map[string]int{
		"pc":    10,  // PIECE
		"cup":   10,  // CUP
		"srv":   20,  // SERVING
		"bowl":  5,   // BOWL
		"plate": 5,   // PLATE
		"slice": 10,  // SLICE
		"kg":    5,   // KG
		"g":     500, // GRAM
		"L":     5,   // LITRE
		"ml":    500, // ML
		"box":   3,   // BOX
		"btl":   5,   // BOTTLE
		"shot":  50,  // SHOT
		"pack":  5,   // PACK
		"bag":   5,   // BAG
	}
}

// InventorySettingsHandler manages typed tenant inventory configuration.
type InventorySettingsHandler struct {
	log      *zap.Logger
	db       *ent.Client
	rbacSvc  *rbac.Service
	treasury *treasury.Client
}

func NewInventorySettingsHandler(log *zap.Logger, db *ent.Client) *InventorySettingsHandler {
	return &InventorySettingsHandler{log: log, db: db}
}

// SetTreasuryClient injects the treasury S2S client used to source the tenant's tax codes.
func (h *InventorySettingsHandler) SetTreasuryClient(c *treasury.Client) {
	h.treasury = c
}

// SetRBACService injects the RBAC service so settings mutations enforce per-action permissions.
func (h *InventorySettingsHandler) SetRBACService(svc *rbac.Service) {
	h.rbacSvc = svc
}

type inventorySettingsResponse struct {
	TenantID string `json:"tenant_id"`
	// Stock thresholds
	LowStockThresholdPct      float64        `json:"low_stock_threshold_pct"`
	CriticalStockThresholdPct float64        `json:"critical_stock_threshold_pct"`
	DefaultReorderLevel       int            `json:"default_reorder_level"`
	UnitReorderDefaults       map[string]int `json:"unit_reorder_defaults"`
	ExpiryWarningDays         int            `json:"expiry_warning_days"`
	// Notifications
	EnableLowStockNotifications bool    `json:"enable_low_stock_notifications"`
	EnableExpiryNotifications   bool    `json:"enable_expiry_notifications"`
	NotificationEmail           *string `json:"notification_email"`
	DefaultWarehouseID          *string `json:"default_warehouse_id"`
	// Tracking
	EnableLotTracking             bool `json:"enable_lot_tracking"`
	EnableExpiryTracking          bool `json:"enable_expiry_tracking"`
	PurchaseOrderApprovalRequired bool `json:"purchase_order_approval_required"`
	AutoAdjustOnTransfer          bool `json:"auto_adjust_on_transfer"`
	// Non-depletion (manual stock tracking) policy
	RecipeItemsNonDepletingDefault bool `json:"recipe_items_non_depleting_default"`
	RecordTheoreticalUsage         bool `json:"record_theoretical_usage"`
	// Modules
	LotsModuleEnabled         bool `json:"lots_module_enabled"`
	RecipesModuleEnabled      bool `json:"recipes_module_enabled"`
	PurchaseOrdersEnabled     bool `json:"purchase_orders_enabled"`
	SupplierManagementEnabled bool `json:"supplier_management_enabled"`
	// Hospitality modules
	EnableRoomPricing        bool `json:"enable_room_pricing"`
	EnableFacilityBooking    bool `json:"enable_facility_booking"`
	EnableConferencePackages bool `json:"enable_conference_packages"`
	// Costing & tax / compliance
	CostingMethod              string   `json:"costing_method"` // wavg|fifo|lifo|fefo
	DefaultTargetMarginPercent *float64 `json:"default_target_margin_percent"`
	PricesInclusiveOfTax       bool     `json:"prices_inclusive_of_tax"`
	DefaultTaxCode             string   `json:"default_tax_code"`
	UpdatedAt                  string   `json:"updated_at"`
}

func toInventorySettingsResponse(c *ent.TenantInventoryConfig) inventorySettingsResponse {
	unitDefaults := c.UnitReorderDefaults
	if unitDefaults == nil {
		unitDefaults = DefaultUnitReorderLevels()
	}
	return inventorySettingsResponse{
		TenantID:                      c.TenantID.String(),
		LowStockThresholdPct:          c.LowStockThresholdPct,
		CriticalStockThresholdPct:     c.CriticalStockThresholdPct,
		DefaultReorderLevel:           c.DefaultReorderLevel,
		UnitReorderDefaults:           unitDefaults,
		ExpiryWarningDays:             c.ExpiryWarningDays,
		EnableLowStockNotifications:   c.EnableLowStockNotifications,
		EnableExpiryNotifications:     c.EnableExpiryNotifications,
		NotificationEmail:             c.NotificationEmail,
		DefaultWarehouseID:            c.DefaultWarehouseID,
		EnableLotTracking:             c.EnableLotTracking,
		EnableExpiryTracking:          c.EnableExpiryTracking,
		PurchaseOrderApprovalRequired: c.PurchaseOrderApprovalRequired,
		AutoAdjustOnTransfer:          c.AutoAdjustOnTransfer,
		RecipeItemsNonDepletingDefault: c.RecipeItemsNonDepletingDefault,
		RecordTheoreticalUsage:         c.RecordTheoreticalUsage,
		LotsModuleEnabled:             c.LotsModuleEnabled,
		RecipesModuleEnabled:          c.RecipesModuleEnabled,
		PurchaseOrdersEnabled:         c.PurchaseOrdersEnabled,
		SupplierManagementEnabled:     c.SupplierManagementEnabled,
		EnableRoomPricing:             c.EnableRoomPricing,
		EnableFacilityBooking:         c.EnableFacilityBooking,
		EnableConferencePackages:      c.EnableConferencePackages,
		CostingMethod:                 c.CostingMethod.String(),
		DefaultTargetMarginPercent:    c.DefaultTargetMarginPercent,
		PricesInclusiveOfTax:          c.PricesInclusiveOfTax,
		DefaultTaxCode:                c.DefaultTaxCode,
		UpdatedAt:                     c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func (h *InventorySettingsHandler) getOrCreate(r *http.Request, tenantID uuid.UUID) (*ent.TenantInventoryConfig, error) {
	ctx := r.Context()
	cfg, err := h.db.TenantInventoryConfig.Query().
		Where(entconfig.TenantID(tenantID)).
		Only(ctx)
	if err == nil {
		return cfg, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return h.db.TenantInventoryConfig.Create().
		SetTenantID(tenantID).
		Save(ctx)
}

// GetSettings handles GET /{tenant}/inventory/settings
func (h *InventorySettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}
	cfg, err := h.getOrCreate(r, tenantID)
	if err != nil {
		h.log.Error("get inventory settings", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")
		return
	}
	writeJSON(w, http.StatusOK, toInventorySettingsResponse(cfg))
}

type updateInventorySettingsInput struct {
	LowStockThresholdPct          *float64       `json:"low_stock_threshold_pct"`
	CriticalStockThresholdPct     *float64       `json:"critical_stock_threshold_pct"`
	DefaultReorderLevel           *int           `json:"default_reorder_level"`
	UnitReorderDefaults           map[string]int `json:"unit_reorder_defaults"`
	ExpiryWarningDays             *int           `json:"expiry_warning_days"`
	EnableLowStockNotifications   *bool          `json:"enable_low_stock_notifications"`
	EnableExpiryNotifications     *bool          `json:"enable_expiry_notifications"`
	NotificationEmail             *string        `json:"notification_email"`
	DefaultWarehouseID            *string        `json:"default_warehouse_id"`
	EnableLotTracking             *bool          `json:"enable_lot_tracking"`
	EnableExpiryTracking          *bool          `json:"enable_expiry_tracking"`
	PurchaseOrderApprovalRequired *bool          `json:"purchase_order_approval_required"`
	AutoAdjustOnTransfer          *bool          `json:"auto_adjust_on_transfer"`
	RecipeItemsNonDepletingDefault *bool         `json:"recipe_items_non_depleting_default"`
	RecordTheoreticalUsage         *bool         `json:"record_theoretical_usage"`
	EnableRoomPricing             *bool          `json:"enable_room_pricing"`
	EnableFacilityBooking         *bool          `json:"enable_facility_booking"`
	EnableConferencePackages      *bool          `json:"enable_conference_packages"`
	DefaultTargetMarginPercent    *float64       `json:"default_target_margin_percent"`
	PricesInclusiveOfTax          *bool          `json:"prices_inclusive_of_tax"`
	DefaultTaxCode                *string        `json:"default_tax_code"`
	CostingMethod                 *string        `json:"costing_method"`
}

// PutSettings handles PUT /{tenant}/inventory/settings
func (h *InventorySettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}

	var input updateInventorySettingsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}

	cfg, err := h.getOrCreate(r, tenantID)
	if err != nil {
		h.log.Error("get inventory settings for update", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")
		return
	}

	upd := cfg.Update()
	if input.LowStockThresholdPct != nil {
		upd = upd.SetLowStockThresholdPct(*input.LowStockThresholdPct)
	}
	if input.CriticalStockThresholdPct != nil {
		upd = upd.SetCriticalStockThresholdPct(*input.CriticalStockThresholdPct)
	}
	if input.DefaultReorderLevel != nil {
		upd = upd.SetDefaultReorderLevel(*input.DefaultReorderLevel)
	}
	if input.UnitReorderDefaults != nil {
		upd = upd.SetUnitReorderDefaults(input.UnitReorderDefaults)
	}
	if input.ExpiryWarningDays != nil {
		upd = upd.SetExpiryWarningDays(*input.ExpiryWarningDays)
	}
	if input.EnableLowStockNotifications != nil {
		upd = upd.SetEnableLowStockNotifications(*input.EnableLowStockNotifications)
	}
	if input.EnableExpiryNotifications != nil {
		upd = upd.SetEnableExpiryNotifications(*input.EnableExpiryNotifications)
	}
	if input.NotificationEmail != nil {
		upd = upd.SetNotificationEmail(*input.NotificationEmail)
	}
	if input.DefaultWarehouseID != nil {
		upd = upd.SetDefaultWarehouseID(*input.DefaultWarehouseID)
	}
	if input.EnableLotTracking != nil {
		upd = upd.SetEnableLotTracking(*input.EnableLotTracking)
	}
	if input.EnableExpiryTracking != nil {
		upd = upd.SetEnableExpiryTracking(*input.EnableExpiryTracking)
	}
	if input.PurchaseOrderApprovalRequired != nil {
		upd = upd.SetPurchaseOrderApprovalRequired(*input.PurchaseOrderApprovalRequired)
	}
	if input.AutoAdjustOnTransfer != nil {
		upd = upd.SetAutoAdjustOnTransfer(*input.AutoAdjustOnTransfer)
	}
	if input.RecipeItemsNonDepletingDefault != nil {
		upd = upd.SetRecipeItemsNonDepletingDefault(*input.RecipeItemsNonDepletingDefault)
	}
	if input.RecordTheoreticalUsage != nil {
		upd = upd.SetRecordTheoreticalUsage(*input.RecordTheoreticalUsage)
	}
	if input.CostingMethod != nil {
		upd = upd.SetCostingMethod(entconfig.CostingMethod(*input.CostingMethod))
	}
	if input.EnableRoomPricing != nil {
		upd = upd.SetEnableRoomPricing(*input.EnableRoomPricing)
	}
	if input.EnableFacilityBooking != nil {
		upd = upd.SetEnableFacilityBooking(*input.EnableFacilityBooking)
	}
	if input.EnableConferencePackages != nil {
		upd = upd.SetEnableConferencePackages(*input.EnableConferencePackages)
	}
	if input.DefaultTargetMarginPercent != nil {
		upd = upd.SetDefaultTargetMarginPercent(*input.DefaultTargetMarginPercent)
	}
	if input.PricesInclusiveOfTax != nil {
		upd = upd.SetPricesInclusiveOfTax(*input.PricesInclusiveOfTax)
	}
	if input.DefaultTaxCode != nil {
		upd = upd.SetDefaultTaxCode(*input.DefaultTaxCode)
	}

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("update inventory settings", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to save settings")
		return
	}
	writeJSON(w, http.StatusOK, toInventorySettingsResponse(updated))
}

type updateInventoryModulesInput struct {
	LotsModuleEnabled         *bool `json:"lots_module_enabled"`
	RecipesModuleEnabled      *bool `json:"recipes_module_enabled"`
	PurchaseOrdersEnabled     *bool `json:"purchase_orders_enabled"`
	SupplierManagementEnabled *bool `json:"supplier_management_enabled"`
}

// PatchModules handles PATCH /{tenant}/inventory/settings/modules
func (h *InventorySettingsHandler) PatchModules(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}

	var input updateInventoryModulesInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}

	cfg, err := h.getOrCreate(r, tenantID)
	if err != nil {
		h.log.Error("get inventory settings for module patch", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")
		return
	}

	upd := cfg.Update()
	if input.LotsModuleEnabled != nil {
		upd = upd.SetLotsModuleEnabled(*input.LotsModuleEnabled)
	}
	if input.RecipesModuleEnabled != nil {
		upd = upd.SetRecipesModuleEnabled(*input.RecipesModuleEnabled)
	}
	if input.PurchaseOrdersEnabled != nil {
		upd = upd.SetPurchaseOrdersEnabled(*input.PurchaseOrdersEnabled)
	}
	if input.SupplierManagementEnabled != nil {
		upd = upd.SetSupplierManagementEnabled(*input.SupplierManagementEnabled)
	}

	updated, err := upd.Save(r.Context())
	if err != nil {
		h.log.Error("patch inventory modules", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to save module settings")
		return
	}
	writeJSON(w, http.StatusOK, toInventorySettingsResponse(updated))
}

// taxCodeDTO is the inventory-ui-facing shape for a treasury tax code (rates are sourced
// from treasury-api; inventory never stores them).
type taxCodeDTO struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Rate      float64 `json:"rate"`
	TaxType   string  `json:"tax_type"`
	IsDefault bool    `json:"is_default"`
}

// ListTaxes handles GET /{tenant}/inventory/taxes — the tenant's active tax codes, sourced
// and cached from treasury-api (the platform source of truth). Powers the tax-code picker in
// inventory-ui. Returns an empty list (never an error) when treasury is unavailable so the UI
// degrades gracefully to manual entry.
func (h *InventorySettingsHandler) ListTaxes(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant", "invalid tenant ID")
		return
	}

	out := []taxCodeDTO{}
	if h.treasury != nil {
		codes, tErr := h.treasury.GetTaxCodes(r.Context(), tenantID)
		if tErr != nil {
			h.log.Warn("list taxes: treasury unavailable", zap.Error(tErr))
		}
		for _, t := range codes {
			if !t.IsActive {
				continue
			}
			out = append(out, taxCodeDTO{
				Code:      t.Code,
				Name:      t.Name,
				Rate:      float64(t.Rate),
				TaxType:   t.TaxType,
				IsDefault: t.IsDefault,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tax_codes": out, "total": len(out)})
}

// RegisterRoutes registers typed inventory settings routes under the tenant group.
// GET stays open to any authenticated tenant user; mutations require settings-change
// permission (platform owners bypass via the RBAC middleware).
func (h *InventorySettingsHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Get("/inventory/settings", h.GetSettings)
	r.With(perm(rbac.PermSettingsChange)).Put("/inventory/settings", h.PutSettings)
	r.With(perm(rbac.PermSettingsChange)).Patch("/inventory/settings/modules", h.PatchModules)
	// Tax codes for the settings + item tax-code pickers (read-only mirror of treasury-api).
	r.Get("/inventory/taxes", h.ListTaxes)
	// Bank list + account verification (proxied to treasury S2S Paystack) so supplier bank
	// details are verified before saving — one source of truth across services.
	r.Get("/inventory/banks/{country}", h.ListBanks)
	r.Get("/inventory/banks/resolve", h.ResolveBankAccount)
}

// ListBanks proxies the Paystack bank list for a country via treasury S2S.
func (h *InventorySettingsHandler) ListBanks(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.treasury == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "bank verification not configured")
		return
	}
	raw, err := h.treasury.ListBanks(r.Context(), tenantID, chi.URLParam(r, "country"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "BANKS_FAILED", "failed to load banks")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ResolveBankAccount proxies Paystack account name-enquiry via treasury S2S.
func (h *InventorySettingsHandler) ResolveBankAccount(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	if h.treasury == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "bank verification not configured")
		return
	}
	raw, err := h.treasury.ResolveAccount(r.Context(), tenantID, r.URL.Query().Get("account_number"), r.URL.Query().Get("bank_code"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "RESOLVE_FAILED", "failed to verify account")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
