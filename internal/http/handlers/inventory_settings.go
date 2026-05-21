package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

// InventorySettingsHandler manages typed tenant inventory configuration.
type InventorySettingsHandler struct {
	log *zap.Logger
	db  *ent.Client
}

func NewInventorySettingsHandler(log *zap.Logger, db *ent.Client) *InventorySettingsHandler {
	return &InventorySettingsHandler{log: log, db: db}
}

type inventorySettingsResponse struct {
	TenantID string `json:"tenant_id"`
	// Stock thresholds
	LowStockThresholdPct      float64 `json:"low_stock_threshold_pct"`
	CriticalStockThresholdPct float64 `json:"critical_stock_threshold_pct"`
	DefaultReorderLevel       int     `json:"default_reorder_level"`
	ExpiryWarningDays         int     `json:"expiry_warning_days"`
	// Notifications
	EnableLowStockNotifications bool    `json:"enable_low_stock_notifications"`
	EnableExpiryNotifications   bool    `json:"enable_expiry_notifications"`
	NotificationEmail           *string `json:"notification_email"`
	DefaultWarehouseID          *string `json:"default_warehouse_id"`
	// Tracking
	EnableLotTracking              bool `json:"enable_lot_tracking"`
	EnableExpiryTracking           bool `json:"enable_expiry_tracking"`
	PurchaseOrderApprovalRequired  bool `json:"purchase_order_approval_required"`
	AutoAdjustOnTransfer           bool `json:"auto_adjust_on_transfer"`
	// Modules
	LotsModuleEnabled          bool `json:"lots_module_enabled"`
	RecipesModuleEnabled       bool `json:"recipes_module_enabled"`
	PurchaseOrdersEnabled      bool `json:"purchase_orders_enabled"`
	SupplierManagementEnabled  bool `json:"supplier_management_enabled"`
	UpdatedAt                  string `json:"updated_at"`
}

func toInventorySettingsResponse(c *ent.TenantInventoryConfig) inventorySettingsResponse {
	return inventorySettingsResponse{
		TenantID:                      c.TenantID.String(),
		LowStockThresholdPct:          c.LowStockThresholdPct,
		CriticalStockThresholdPct:     c.CriticalStockThresholdPct,
		DefaultReorderLevel:           c.DefaultReorderLevel,
		ExpiryWarningDays:             c.ExpiryWarningDays,
		EnableLowStockNotifications:   c.EnableLowStockNotifications,
		EnableExpiryNotifications:     c.EnableExpiryNotifications,
		NotificationEmail:             c.NotificationEmail,
		DefaultWarehouseID:            c.DefaultWarehouseID,
		EnableLotTracking:             c.EnableLotTracking,
		EnableExpiryTracking:          c.EnableExpiryTracking,
		PurchaseOrderApprovalRequired: c.PurchaseOrderApprovalRequired,
		AutoAdjustOnTransfer:          c.AutoAdjustOnTransfer,
		LotsModuleEnabled:             c.LotsModuleEnabled,
		RecipesModuleEnabled:          c.RecipesModuleEnabled,
		PurchaseOrdersEnabled:         c.PurchaseOrdersEnabled,
		SupplierManagementEnabled:     c.SupplierManagementEnabled,
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
	LowStockThresholdPct          *float64 `json:"low_stock_threshold_pct"`
	CriticalStockThresholdPct     *float64 `json:"critical_stock_threshold_pct"`
	DefaultReorderLevel           *int     `json:"default_reorder_level"`
	ExpiryWarningDays             *int     `json:"expiry_warning_days"`
	EnableLowStockNotifications   *bool    `json:"enable_low_stock_notifications"`
	EnableExpiryNotifications     *bool    `json:"enable_expiry_notifications"`
	NotificationEmail             *string  `json:"notification_email"`
	DefaultWarehouseID            *string  `json:"default_warehouse_id"`
	EnableLotTracking             *bool    `json:"enable_lot_tracking"`
	EnableExpiryTracking          *bool    `json:"enable_expiry_tracking"`
	PurchaseOrderApprovalRequired *bool    `json:"purchase_order_approval_required"`
	AutoAdjustOnTransfer          *bool    `json:"auto_adjust_on_transfer"`
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

// RegisterRoutes registers typed inventory settings routes under the tenant group.
func (h *InventorySettingsHandler) RegisterRoutes(r chi.Router) {
	r.Get("/inventory/settings", h.GetSettings)
	r.Put("/inventory/settings", h.PutSettings)
	r.Patch("/inventory/settings/modules", h.PatchModules)
}

