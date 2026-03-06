package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// ItemsServicer defines the contract for item availability operations.
type ItemsServicer interface {
	GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*items.StockAvailability, error)
	BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.StockAvailability, error)
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

// InventoryHandler handles all inventory-related HTTP endpoints.
type InventoryHandler struct {
	log      *zap.Logger
	itemsSvc ItemsServicer
	stockSvc StockServicer
}

// NewInventoryHandler creates a new inventory handler.
func NewInventoryHandler(log *zap.Logger, itemsSvc ItemsServicer, stockSvc StockServicer) *InventoryHandler {
	return &InventoryHandler{
		log:      log.Named("inventory.handler"),
		itemsSvc: itemsSvc,
		stockSvc: stockSvc,
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
		// Item availability
		inv.Get("/items/{sku}", h.GetStockAvailability)
		inv.Post("/availability", h.BulkAvailability)

		// Reservations
		inv.Post("/reservations", h.CreateReservation)
		inv.Get("/reservations", h.GetReservationsByOrder)
		inv.Get("/reservations/{reservationID}", h.GetReservation)
		inv.Post("/reservations/{reservationID}/release", h.ReleaseReservation)
		inv.Post("/reservations/{reservationID}/consume", h.ConsumeReservation)

		// Consumption
		inv.Post("/consumption", h.RecordConsumption)
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
