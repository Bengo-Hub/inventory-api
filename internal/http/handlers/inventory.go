package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// ItemsServicer defines the contract for item availability and CRUD operations.
type ItemsServicer interface {
	GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*items.StockAvailability, error)
	BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.StockAvailability, error)
	GetBOMAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.BOMAvailabilityResult, error)
	GetInventorySummary(ctx context.Context, tenantID uuid.UUID) (*items.InventorySummary, error)
	CreateItem(ctx context.Context, tenantID uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	UpdateItem(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	ListItems(ctx context.Context, tenantID uuid.UUID, typeFilter, statusFilter string, limit, offset int, categoryID *uuid.UUID, unitID *uuid.UUID, search string, outletID *uuid.UUID, tagsFilter ...string) ([]items.ItemDTO, int, error)
	ListEventItems(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]items.ItemDTO, int, error)
	ListCategories(ctx context.Context, tenantID uuid.UUID) ([]items.CategoryDTO, error)
	CreateCategory(ctx context.Context, tenantID uuid.UUID, dto items.CategoryDTO) (*items.CategoryDTO, error)
	UpdateCategory(ctx context.Context, tenantID, id uuid.UUID, dto items.CategoryDTO) (*items.CategoryDTO, error)
	DeleteCategory(ctx context.Context, tenantID, id uuid.UUID) error
}

// StockServicer defines the contract for stock reservation and consumption operations.
type StockServicer interface {
	CreateReservation(ctx context.Context, tenantID uuid.UUID, req stock.ReservationRequest) (*stock.ReservationResponse, error)
	GetReservation(ctx context.Context, tenantID, reservationID uuid.UUID) (*stock.ReservationResponse, error)
	GetReservationsByOrderID(ctx context.Context, tenantID, orderID uuid.UUID) ([]stock.ReservationResponse, error)
	ReleaseReservation(ctx context.Context, tenantID, reservationID uuid.UUID, reason string) error
	ConsumeReservation(ctx context.Context, tenantID, reservationID uuid.UUID) error
	RecordConsumption(ctx context.Context, tenantID uuid.UUID, req stock.ConsumptionRequest) (*stock.ConsumptionResponse, error)
	AdjustStock(ctx context.Context, tenantID uuid.UUID, req stock.AdjustStockRequest) (*stock.AdjustStockResponse, error)
	ListAdjustments(ctx context.Context, tenantID uuid.UUID, req stock.ListAdjustmentsRequest) ([]stock.StockAdjustmentDTO, error)
}

// RecipesServicer defines the contract for recipe management.
type RecipesServicer interface {
	ListRecipes(ctx context.Context, tenantID uuid.UUID, limit, offset int, outletID *uuid.UUID) ([]recipes.RecipeDTO, int, error)
	GetRecipe(ctx context.Context, tenantID, id uuid.UUID) (*recipes.RecipeDTO, error)
	CreateRecipe(ctx context.Context, tenantID uuid.UUID, dto recipes.RecipeDTO) (*recipes.RecipeDTO, error)
	UpdateRecipe(ctx context.Context, tenantID uuid.UUID, recipeID uuid.UUID, dto recipes.RecipeDTO) (*recipes.RecipeDTO, error)
	DeleteRecipe(ctx context.Context, tenantID uuid.UUID, recipeID uuid.UUID) error
	GetRecipeBySKU(ctx context.Context, tenantID uuid.UUID, sku string) (*recipes.RecipeDTO, error)
}

// UnitsServicer defines the contract for unit management.
type UnitsServicer interface {
	ListUnits(ctx context.Context, tenantID uuid.UUID) ([]units.UnitDTO, error)
	CreateUnit(ctx context.Context, tenantID uuid.UUID, dto units.UnitDTO) (*units.UnitDTO, error)
	UpdateUnit(ctx context.Context, tenantID, id uuid.UUID, dto units.UnitDTO) (*units.UnitDTO, error)
	DeleteUnit(ctx context.Context, tenantID, id uuid.UUID) error
}

// ModifiersServicer defines the contract for modifier group/option management.
type ModifiersServicer interface {
	ListAllModifierGroups(ctx context.Context, tenantID uuid.UUID, limit, offset int, search string) ([]modifiers.ModifierGroupDTO, int, error)
	GetModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID) (*modifiers.ModifierGroupDTO, error)
	ListModifierGroups(ctx context.Context, tenantID, itemID uuid.UUID) ([]modifiers.ModifierGroupDTO, error)
	CreateModifierGroup(ctx context.Context, tenantID uuid.UUID, req modifiers.CreateModifierGroupRequest) (*modifiers.ModifierGroupDTO, error)
	UpdateModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID, req modifiers.UpdateModifierGroupRequest) (*modifiers.ModifierGroupDTO, error)
	DeleteModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID) error
	CreateModifierOption(ctx context.Context, tenantID, groupID uuid.UUID, req modifiers.CreateModifierOptionRequest) (*modifiers.ModifierOptionDTO, error)
	UpdateModifierOption(ctx context.Context, tenantID, optionID uuid.UUID, req modifiers.UpdateModifierOptionRequest) (*modifiers.ModifierOptionDTO, error)
	DeleteModifierOption(ctx context.Context, tenantID, optionID uuid.UUID) error
}

// InventoryHandler handles all inventory-related HTTP endpoints.
type InventoryHandler struct {
	log          *zap.Logger
	itemsSvc     ItemsServicer
	stockSvc     StockServicer
	recipeSvc    RecipesServicer
	unitSvc      UnitsServicer
	modifiersSvc ModifiersServicer
	rbacSvc      *rbac.Service
}

// NewInventoryHandler creates a new inventory handler.
func NewInventoryHandler(log *zap.Logger, itemsSvc ItemsServicer, stockSvc StockServicer, recipeSvc RecipesServicer, unitSvc UnitsServicer) *InventoryHandler {
	return &InventoryHandler{
		log:       log.Named("inventory.handler"),
		itemsSvc:  itemsSvc,
		stockSvc:  stockSvc,
		recipeSvc: recipeSvc,
		unitSvc:   unitSvc,
	}
}

// SetRBACService injects the RBAC service for per-route permission enforcement.
// When set, mutation routes require the corresponding inventory.*.{action} permission.
func (h *InventoryHandler) SetRBACService(svc *rbac.Service) {
	h.rbacSvc = svc
}

// SetModifiersService injects the modifiers service (optional; modifier endpoints are skipped if nil).
func (h *InventoryHandler) SetModifiersService(svc ModifiersServicer) {
	h.modifiersSvc = svc
}

// parseTenantID is now defined in tenant.go with platform-owner override support.

// RegisterRoutes wires inventory routes onto the given chi.Router.
// When rbacSvc is set (via SetRBACService), mutation routes enforce per-action permissions.
func (h *InventoryHandler) RegisterRoutes(r chi.Router) {
	// perm returns a per-route permission middleware when rbacSvc is set, or a pass-through.
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory", func(inv chi.Router) {
		// Items
		inv.Get("/items", h.ListItems)
		inv.With(perm(rbac.PermItemsAdd)).Post("/items", h.CreateItem)
		inv.Get("/items/{sku}", h.GetStockAvailability)
		inv.With(perm(rbac.PermItemsChange)).Put("/items/{sku}", h.UpdateItem)
		inv.With(perm(rbac.PermItemsDelete)).Delete("/items/{sku}", h.DeleteItem)

		// Availability
		inv.Post("/availability", h.BulkAvailability)
		inv.Get("/availability/bom", h.GetBOMAvailability)

		// Stock adjustments
		inv.With(perm(rbac.PermStockAdd)).Post("/adjust", h.AdjustStock)
		inv.With(perm(rbac.PermStockAdd)).Post("/adjustments", h.CreateAdjustment)
		inv.Get("/adjustments", h.ListAdjustments)

		// Categories
		inv.Get("/categories", h.ListCategories)
		inv.With(perm(rbac.PermItemsAdd)).Post("/categories", h.CreateCategory)
		inv.With(perm(rbac.PermItemsChange)).Put("/categories/{categoryID}", h.UpdateCategory)
		inv.With(perm(rbac.PermItemsDelete)).Delete("/categories/{categoryID}", h.DeleteCategory)

		// Reservations
		inv.With(perm(rbac.PermReservationsAdd)).Post("/reservations", h.CreateReservation)
		inv.Get("/reservations", h.GetReservationsByOrder)
		inv.Get("/reservations/{reservationID}", h.GetReservation)
		inv.With(perm(rbac.PermReservationsChange)).Post("/reservations/{reservationID}/release", h.ReleaseReservation)
		inv.With(perm(rbac.PermReservationsChange)).Post("/reservations/{reservationID}/consume", h.ConsumeReservation)

		// Consumption
		inv.With(perm(rbac.PermConsumptionsAdd)).Post("/consumption", h.RecordConsumption)

		// Summary
		inv.Get("/summary", h.GetInventorySummary)

		// Recipes — hospitality and quick_service outlets only
		inv.Group(func(rec chi.Router) {
			rec.Use(invmiddleware.RequireOutletUseCase("hospitality", "quick_service"))
			rec.Get("/recipes", h.ListRecipes)
			rec.With(perm(rbac.PermRecipesAdd)).Post("/recipes", h.CreateRecipe)
			rec.Get("/recipes/{recipeID}", h.GetRecipe)
			rec.With(perm(rbac.PermRecipesChange)).Put("/recipes/{recipeID}", h.UpdateRecipe)
			rec.With(perm(rbac.PermRecipesDelete)).Delete("/recipes/{recipeID}", h.DeleteRecipe)
		})

		// Events — SERVICE-type items with event_start_at set
		inv.Get("/events", h.ListEventItems)

		// Units (manage is platform-only; view is open)
		inv.Get("/units", h.ListUnits)
		inv.With(perm(rbac.PermUnitsAdd)).Post("/units", h.CreateUnit)
		inv.With(perm(rbac.PermUnitsChange)).Put("/units/{unitID}", h.UpdateUnit)
		inv.With(perm(rbac.PermUnitsDelete)).Delete("/units/{unitID}", h.DeleteUnit)

		// CSV bulk import
		inv.With(perm(rbac.PermItemsAdd)).Post("/items/import", h.ImportItems)

		// Modifier Groups & Options
		inv.Get("/modifier-groups", h.ListAllModifierGroups)
		inv.Get("/modifier-groups/{id}", h.GetModifierGroup)
		inv.Get("/items/{itemId}/modifier-groups", h.ListModifierGroups)
		inv.With(perm(rbac.PermVariantsAdd)).Post("/modifier-groups", h.CreateModifierGroup)
		inv.With(perm(rbac.PermVariantsChange)).Put("/modifier-groups/{id}", h.UpdateModifierGroup)
		inv.With(perm(rbac.PermVariantsDelete)).Delete("/modifier-groups/{id}", h.DeleteModifierGroup)
		inv.With(perm(rbac.PermVariantsAdd)).Post("/modifier-groups/{id}/options", h.CreateModifierOption)
		inv.With(perm(rbac.PermVariantsChange)).Put("/modifier-options/{id}", h.UpdateModifierOption)
		inv.With(perm(rbac.PermVariantsDelete)).Delete("/modifier-options/{id}", h.DeleteModifierOption)
	})
}

// GetStockAvailability handles GET /v1/{tenant}/inventory/items/{sku}
func (h *InventoryHandler) GetStockAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, sku)
	if err != nil {
		h.log.Error("get stock availability failed", zap.Error(err), zap.String("sku", sku))
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, avail)
}

// BulkAvailability handles POST /v1/{tenant}/inventory/availability
func (h *InventoryHandler) BulkAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req struct {
		SKUs []string `json:"skus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if len(req.SKUs) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "At least one SKU is required")
		return
	}

	results, err := h.itemsSvc.BulkAvailability(r.Context(), tenantID, req.SKUs)
	if err != nil {
		h.log.Error("bulk availability failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to check availability")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateReservation handles POST /v1/{tenant}/inventory/reservations
func (h *InventoryHandler) CreateReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ReservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	req.TenantID = tenantID

	if req.OrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "Order ID is required")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_ITEMS", "At least one item is required")
		return
	}

	result, err := h.stockSvc.CreateReservation(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RESERVATION_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// GetReservation handles GET /v1/{tenant}/inventory/reservations/{reservationID}
func (h *InventoryHandler) GetReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	result, err := h.stockSvc.GetReservation(r.Context(), tenantID, reservationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetReservationsByOrder handles GET /v1/{tenant}/inventory/reservations?order_id={id}
func (h *InventoryHandler) GetReservationsByOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	orderIDStr := r.URL.Query().Get("order_id")
	if orderIDStr == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "order_id query parameter is required")
		return
	}

	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ORDER_ID", "Invalid order_id")
		return
	}

	results, err := h.stockSvc.GetReservationsByOrderID(r.Context(), tenantID, orderID)
	if err != nil {
		h.log.Error("get reservations by order failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// ReleaseReservation handles POST /v1/{tenant}/inventory/reservations/{reservationID}/release
func (h *InventoryHandler) ReleaseReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if err := h.stockSvc.ReleaseReservation(r.Context(), tenantID, reservationID, req.Reason); err != nil {
		h.log.Error("release reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RELEASE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// ConsumeReservation handles POST /v1/{tenant}/inventory/reservations/{reservationID}/consume
func (h *InventoryHandler) ConsumeReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	if err := h.stockSvc.ConsumeReservation(r.Context(), tenantID, reservationID); err != nil {
		h.log.Error("consume reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CONSUME_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "consumed"})
}

// RecordConsumption handles POST /v1/{tenant}/inventory/consumption
func (h *InventoryHandler) RecordConsumption(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ConsumptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	req.TenantID = tenantID

	if req.OrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "Order ID is required")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_ITEMS", "At least one item is required")
		return
	}

	result, err := h.stockSvc.RecordConsumption(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("record consumption failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CONSUMPTION_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// ListRecipes handles GET /v1/{tenant}/inventory/recipes
func (h *InventoryHandler) ListRecipes(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := r.URL.Query().Get("sku")
	if sku != "" {
		recipe, err := h.recipeSvc.GetRecipeBySKU(r.Context(), tenantID, sku)
		if err != nil {
			if ent.IsNotFound(err) {
				writeJSON(w, http.StatusOK, []recipes.RecipeDTO{})
				return
			}
			h.log.Error("get recipe by sku failed", zap.Error(err), zap.String("sku", sku))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to fetch recipe")
			return
		}
		writeJSON(w, http.StatusOK, []recipes.RecipeDTO{*recipe})
		return
	}

	var recipeOutletID *uuid.UUID
	if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		if oid, err := uuid.Parse(outletStr); err == nil {
			recipeOutletID = &oid
		}
	}

	p := pagination.Parse(r)
	results, total, err := h.recipeSvc.ListRecipes(r.Context(), tenantID, p.Limit, p.Offset, recipeOutletID)
	if err != nil {
		h.log.Error("list recipes failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list recipes")
		return
	}

	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// GetRecipe handles GET /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) GetRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	result, err := h.recipeSvc.GetRecipe(r.Context(), tenantID, recipeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// CreateRecipe handles POST /v1/{tenant}/inventory/recipes
func (h *InventoryHandler) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req recipes.RecipeDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.recipeSvc.CreateRecipe(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateRecipe handles PUT /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	var req recipes.RecipeDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.recipeSvc.UpdateRecipe(r.Context(), tenantID, recipeID, req)
	if err != nil {
		h.log.Error("update recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteRecipe handles DELETE /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	if err := h.recipeSvc.DeleteRecipe(r.Context(), tenantID, recipeID); err != nil {
		h.log.Error("delete recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// GetInventorySummary handles GET /v1/{tenant}/inventory/summary
func (h *InventoryHandler) GetInventorySummary(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	summary, err := h.itemsSvc.GetInventorySummary(r.Context(), tenantID)
	if err != nil {
		h.log.Error("get inventory summary failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to fetch inventory summary")
		return
	}

	writeJSON(w, http.StatusOK, summary)
}

// ListUnits handles GET /v1/{tenant}/inventory/units
func (h *InventoryHandler) ListUnits(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	results, err := h.unitSvc.ListUnits(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list units failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list units")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateUnit handles POST /v1/{tenant}/inventory/units
func (h *InventoryHandler) CreateUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req units.UnitDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.unitSvc.CreateUnit(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create unit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateUnit handles PUT /v1/{tenant}/inventory/units/{unitID} — updates a unit of measure.
func (h *InventoryHandler) UpdateUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	unitID, err := uuid.Parse(chi.URLParam(r, "unitID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid unit ID")
		return
	}
	var req units.UnitDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	result, err := h.unitSvc.UpdateUnit(r.Context(), tenantID, unitID, req)
	if err != nil {
		h.log.Error("update unit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ListItems handles GET /v1/{tenant}/inventory/items — returns a paginated list of active items.
func (h *InventoryHandler) ListItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	typeFilter := r.URL.Query().Get("type")
	statusFilter := r.URL.Query().Get("status") // "active" | "inactive" | "all"; default = active
	searchFilter := r.URL.Query().Get("search")

	var categoryID *uuid.UUID
	if catStr := r.URL.Query().Get("category_id"); catStr != "" {
		if catID, parseErr := uuid.Parse(catStr); parseErr == nil {
			categoryID = &catID
		}
	}

	var unitID *uuid.UUID
	if unitStr := r.URL.Query().Get("unit_id"); unitStr != "" {
		if uID, parseErr := uuid.Parse(unitStr); parseErr == nil {
			unitID = &uID
		}
	}

	// Parse optional tags filter: ?tags=vegan,gluten_free
	var tagsFilter []string
	if tagsParam := r.URL.Query().Get("tags"); tagsParam != "" {
		for _, t := range strings.Split(tagsParam, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tagsFilter = append(tagsFilter, t)
			}
		}
	}

	var outletID *uuid.UUID
	if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		if oid, err := uuid.Parse(outletStr); err == nil {
			outletID = &oid
		}
	}

	p := pagination.Parse(r)
	results, total, err := h.itemsSvc.ListItems(r.Context(), tenantID, typeFilter, statusFilter, p.Limit, p.Offset, categoryID, unitID, searchFilter, outletID, tagsFilter...)
	if err != nil {
		h.log.Error("list items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list items")
		return
	}

	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// ListEventItems handles GET /v1/{tenant}/inventory/events
// Returns SERVICE-type items that have event_start_at set, ordered by start time ascending.
func (h *InventoryHandler) ListEventItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	results, total, err := h.itemsSvc.ListEventItems(r.Context(), tenantID, p.Limit, p.Offset)
	if err != nil {
		h.log.Error("list event items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list events")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// CreateItem handles POST /v1/{tenant}/inventory/items — creates a new inventory item.
func (h *InventoryHandler) CreateItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req items.ItemDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	// SKU is optional — auto-generated if empty
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	if req.Type == "" {
		req.Type = "GOODS"
	}
	req.IsActive = true

	result, err := h.itemsSvc.CreateItem(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateItem handles PUT /v1/{tenant}/inventory/items/{sku} — updates an existing item by SKU.
func (h *InventoryHandler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, sku)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}

	var req items.ItemDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.itemsSvc.UpdateItem(r.Context(), tenantID, avail.ItemID, req)
	if err != nil {
		h.log.Error("update item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// AdjustStock handles POST /v1/{tenant}/inventory/adjust — adjusts stock levels for an item.
func (h *InventoryHandler) AdjustStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.AdjustStockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.SKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	if req.Adjustment == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ADJUSTMENT", "Adjustment must be non-zero")
		return
	}
	if req.Reason == "" {
		req.Reason = "adjustment"
	}

	result, err := h.stockSvc.AdjustStock(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("adjust stock failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "ADJUST_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteItem handles DELETE /v1/{tenant}/inventory/items/{sku} — soft-deletes an item by SKU.
func (h *InventoryHandler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, sku)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}

	// Soft-delete: set is_active = false
	deactivated := items.ItemDTO{IsActive: false}
	_, err = h.itemsSvc.UpdateItem(r.Context(), tenantID, avail.ItemID, deactivated)
	if err != nil {
		h.log.Error("delete item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// CreateAdjustment handles POST /v1/{tenant}/inventory/adjustments — creates a stock adjustment with audit trail.
func (h *InventoryHandler) CreateAdjustment(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.AdjustStockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.SKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	if req.Adjustment == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ADJUSTMENT", "Adjustment must be non-zero")
		return
	}
	if req.Reason == "" {
		req.Reason = "other"
	}

	result, err := h.stockSvc.AdjustStock(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create adjustment failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "ADJUST_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// ListAdjustments handles GET /v1/{tenant}/inventory/adjustments — lists stock adjustments with filters.
func (h *InventoryHandler) ListAdjustments(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ListAdjustmentsRequest

	if itemIDStr := r.URL.Query().Get("item_id"); itemIDStr != "" {
		itemID, err := uuid.Parse(itemIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ITEM_ID", "Invalid item_id")
			return
		}
		req.ItemID = itemID
	}

	if whIDStr := r.URL.Query().Get("warehouse_id"); whIDStr != "" {
		whID, err := uuid.Parse(whIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE_ID", "Invalid warehouse_id")
			return
		}
		req.WarehouseID = whID
	}

	if reason := r.URL.Query().Get("reason"); reason != "" {
		req.Reason = reason
	}

	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		t, err := time.Parse(time.RFC3339, dateFrom)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DATE_FROM", "date_from must be RFC3339 format")
			return
		}
		req.DateFrom = t
	}

	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		t, err := time.Parse(time.RFC3339, dateTo)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DATE_TO", "date_to must be RFC3339 format")
			return
		}
		req.DateTo = t
	}

	results, err := h.stockSvc.ListAdjustments(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("list adjustments failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list adjustments")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"total": len(results),
	})
}

// GetBOMAvailability handles GET /v1/{tenant}/inventory/availability/bom?skus=SKU1,SKU2
func (h *InventoryHandler) GetBOMAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	skusParam := r.URL.Query().Get("skus")
	if skusParam == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "skus query parameter is required")
		return
	}

	skus := strings.Split(skusParam, ",")
	if len(skus) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "At least one SKU is required")
		return
	}

	// Trim whitespace from SKUs
	for i := range skus {
		skus[i] = strings.TrimSpace(skus[i])
	}

	results, err := h.itemsSvc.GetBOMAvailability(r.Context(), tenantID, skus)
	if err != nil {
		h.log.Error("bom availability failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to check BOM availability")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// ListCategories handles GET /v1/{tenant}/inventory/categories — returns all active categories.
func (h *InventoryHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	results, err := h.itemsSvc.ListCategories(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list categories failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list categories")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"total": len(results),
	})
}

// DeleteCategory handles DELETE /inventory/categories/{categoryID} — soft-deletes a category.
func (h *InventoryHandler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	categoryID, err := uuid.Parse(chi.URLParam(r, "categoryID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	if err := h.itemsSvc.DeleteCategory(r.Context(), tenantID, categoryID); err != nil {
		h.log.Error("delete category failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Category not found or could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// CreateCategory handles POST /v1/{tenant}/inventory/categories — creates a new item category.
func (h *InventoryHandler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req items.CategoryDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	result, err := h.itemsSvc.CreateCategory(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create category failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// UpdateCategory handles PUT /v1/{tenant}/inventory/categories/{categoryID} — updates an item category.
func (h *InventoryHandler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	categoryID, err := uuid.Parse(chi.URLParam(r, "categoryID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	var req items.CategoryDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	result, err := h.itemsSvc.UpdateCategory(r.Context(), tenantID, categoryID, req)
	if err != nil {
		h.log.Error("update category failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteUnit handles DELETE /inventory/units/{unitID} — soft-deletes a unit of measure.
func (h *InventoryHandler) DeleteUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	unitID, err := uuid.Parse(chi.URLParam(r, "unitID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid unit ID")
		return
	}
	if err := h.unitSvc.DeleteUnit(r.Context(), tenantID, unitID); err != nil {
		h.log.Error("delete unit failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Unit not found or could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type importResult struct {
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// ImportItems handles POST /inventory/items/import — CSV bulk upsert.
// Expected CSV columns (header row required): name, sku, type, category_id, unit_id
func (h *InventoryHandler) ImportItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORM", "Expected multipart/form-data with a 'file' field")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_FILE", "File field 'file' is required")
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CSV", "Failed to read CSV header")
		return
	}

	colIdx := make(map[string]int, len(header))
	for i, col := range header {
		colIdx[strings.ToLower(strings.TrimSpace(col))] = i
	}

	col := func(row []string, name string) string {
		i, ok := colIdx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	// Load all existing items once for SKU→ID lookup (avoids N+1 queries).
	existingItems, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "", "all", 10000, 0, nil, nil, "", nil)
	skuToID := make(map[string]uuid.UUID, len(existingItems))
	for _, it := range existingItems {
		skuToID[it.SKU] = it.ID
	}

	var result importResult
	rows, _ := reader.ReadAll()
	for i, row := range rows {
		name := col(row, "name")
		sku := col(row, "sku")
		if name == "" || sku == "" {
			result.Failed++
			result.Errors = append(result.Errors, "row "+strings.Join([]string{}, "")+sku+": name and sku are required")
			_ = i
			continue
		}

		itemType := strings.ToUpper(col(row, "type"))
		if itemType == "" {
			itemType = "GOODS"
		}

		dto := items.ItemDTO{SKU: sku, Name: name, Type: itemType, IsActive: true}
		if catStr := col(row, "category_id"); catStr != "" {
			if catID, parseErr := uuid.Parse(catStr); parseErr == nil {
				dto.CategoryID = &catID
			}
		}
		if unitStr := col(row, "unit_id"); unitStr != "" {
			if unitID, parseErr := uuid.Parse(unitStr); parseErr == nil {
				dto.UnitID = &unitID
			}
		}

		if existingID, exists := skuToID[sku]; exists {
			if _, updateErr := h.itemsSvc.UpdateItem(r.Context(), tenantID, existingID, dto); updateErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, "sku="+sku+": "+updateErr.Error())
			} else {
				result.Updated++
			}
		} else {
			if created, createErr := h.itemsSvc.CreateItem(r.Context(), tenantID, dto); createErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, "sku="+sku+": "+createErr.Error())
			} else {
				skuToID[sku] = created.ID
				result.Created++
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}
