package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// ItemsServicer defines the contract for item availability and CRUD operations.
type ItemsServicer interface {
	GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*items.StockAvailability, error)
	BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.StockAvailability, error)
	GetInventorySummary(ctx context.Context, tenantID uuid.UUID) (*items.InventorySummary, error)
	CreateItem(ctx context.Context, tenantID uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	UpdateItem(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	ListItems(ctx context.Context, tenantID uuid.UUID) ([]items.ItemDTO, error)
	ListCategories(ctx context.Context, tenantID uuid.UUID) ([]items.CategoryDTO, error)
}

// StockServicer defines the contract for stock reservation and consumption operations.
type StockServicer interface {
	CreateReservation(ctx context.Context, tenantID uuid.UUID, req stock.ReservationRequest) (*stock.ReservationResponse, error)
	GetReservation(ctx context.Context, tenantID, reservationID uuid.UUID) (*stock.ReservationResponse, error)
	GetReservationsByOrderID(ctx context.Context, tenantID, orderID uuid.UUID) ([]stock.ReservationResponse, error)
	ReleaseReservation(ctx context.Context, tenantID, reservationID uuid.UUID, reason string) error
	ConsumeReservation(ctx context.Context, tenantID, reservationID uuid.UUID) error
	RecordConsumption(ctx context.Context, tenantID uuid.UUID, req stock.ConsumptionRequest) (*stock.ConsumptionResponse, error)
}

// RecipesServicer defines the contract for recipe management.
type RecipesServicer interface {
	ListRecipes(ctx context.Context, tenantID uuid.UUID) ([]recipes.RecipeDTO, error)
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
}

// InventoryHandler handles all inventory-related HTTP endpoints.
type InventoryHandler struct {
	log       *zap.Logger
	itemsSvc  ItemsServicer
	stockSvc  StockServicer
	recipeSvc RecipesServicer
	unitSvc   UnitsServicer
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

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func parseTenantID(r *http.Request) (uuid.UUID, error) {
	raw := chi.URLParam(r, "tenantID")
	if raw == "" {
		return uuid.Nil, nil
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(decoded)
}

// RegisterRoutes wires inventory routes onto the given chi.Router.
func (h *InventoryHandler) RegisterRoutes(r chi.Router) {
	r.Route("/inventory", func(inv chi.Router) {
		// Item CRUD
		inv.Get("/items", h.ListItems)
		inv.Post("/items", h.CreateItem)
		inv.Get("/items/{sku}", h.GetStockAvailability)
		inv.Put("/items/{sku}", h.UpdateItem)
		inv.Post("/availability", h.BulkAvailability)

		// Categories
		inv.Get("/categories", h.ListCategories)

		// Reservations
		inv.Post("/reservations", h.CreateReservation)
		inv.Get("/reservations", h.GetReservationsByOrder)
		inv.Get("/reservations/{reservationID}", h.GetReservation)
		inv.Post("/reservations/{reservationID}/release", h.ReleaseReservation)
		inv.Post("/reservations/{reservationID}/consume", h.ConsumeReservation)

		// Consumption
		inv.Post("/consumption", h.RecordConsumption)

		// Summary
		inv.Get("/summary", h.GetInventorySummary)

		// Recipes
		inv.Get("/recipes", h.ListRecipes)
		inv.Post("/recipes", h.CreateRecipe)
		inv.Get("/recipes/{recipeID}", h.GetRecipe)
		inv.Put("/recipes/{recipeID}", h.UpdateRecipe)
		inv.Delete("/recipes/{recipeID}", h.DeleteRecipe)

		// Units
		inv.Get("/units", h.ListUnits)
		inv.Post("/units", h.CreateUnit)
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

	results, err := h.recipeSvc.ListRecipes(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list recipes failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list recipes")
		return
	}

	writeJSON(w, http.StatusOK, results)
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

// ListItems handles GET /v1/{tenant}/inventory/items — returns all active items for the tenant.
func (h *InventoryHandler) ListItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	results, err := h.itemsSvc.ListItems(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list items")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"total": len(results),
	})
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

	if req.SKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
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
