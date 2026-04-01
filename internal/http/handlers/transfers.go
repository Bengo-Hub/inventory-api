package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/transfers"
)

// TransferServicer defines the contract for transfer operations.
type TransferServicer interface {
	CreateTransfer(ctx context.Context, tenantID uuid.UUID, req transfers.CreateTransferRequest) (*transfers.TransferResponse, error)
	ListTransfers(ctx context.Context, tenantID uuid.UUID, filter transfers.TransferListFilter) ([]transfers.TransferSummary, int, error)
	GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*transfers.TransferResponse, error)
	ShipTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error
	ReceiveTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error
	CancelTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error
}

// TransferHandler handles stock transfer HTTP endpoints.
type TransferHandler struct {
	log         *zap.Logger
	transferSvc TransferServicer
}

// NewTransferHandler creates a new transfer handler.
func NewTransferHandler(log *zap.Logger, transferSvc TransferServicer) *TransferHandler {
	return &TransferHandler{
		log:         log.Named("transfer.handler"),
		transferSvc: transferSvc,
	}
}

// RegisterRoutes wires transfer routes onto the given chi.Router.
func (h *TransferHandler) RegisterRoutes(r chi.Router) {
	r.Route("/inventory/transfers", func(tr chi.Router) {
		tr.Post("/", h.CreateTransfer)
		tr.Get("/", h.ListTransfers)
		tr.Get("/{transferId}", h.GetTransfer)
		tr.Post("/{transferId}/ship", h.ShipTransfer)
		tr.Post("/{transferId}/receive", h.ReceiveTransfer)
		tr.Post("/{transferId}/cancel", h.CancelTransfer)
	})
}

// CreateTransfer handles POST /v1/{tenant}/inventory/transfers
func (h *TransferHandler) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req transfers.CreateTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.SourceWarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "source_warehouse_id is required")
		return
	}
	if req.DestinationWarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "destination_warehouse_id is required")
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "at least one item is required")
		return
	}

	resp, err := h.transferSvc.CreateTransfer(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create transfer failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ListTransfers handles GET /v1/{tenant}/inventory/transfers
func (h *TransferHandler) ListTransfers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	filter := transfers.TransferListFilter{
		Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"),
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		filter.Limit, _ = strconv.Atoi(l)
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		filter.Offset, _ = strconv.Atoi(o)
	}

	items, total, err := h.transferSvc.ListTransfers(r.Context(), tenantID, filter)
	if err != nil {
		h.log.Error("list transfers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
	})
}

// GetTransfer handles GET /v1/{tenant}/inventory/transfers/{transferId}
func (h *TransferHandler) GetTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	transferID, err := uuid.Parse(chi.URLParam(r, "transferId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid transfer ID")
		return
	}

	resp, err := h.transferSvc.GetTransfer(r.Context(), tenantID, transferID)
	if err != nil {
		h.log.Error("get transfer failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ShipTransfer handles POST /v1/{tenant}/inventory/transfers/{transferId}/ship
func (h *TransferHandler) ShipTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	transferID, err := uuid.Parse(chi.URLParam(r, "transferId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid transfer ID")
		return
	}

	if err := h.transferSvc.ShipTransfer(r.Context(), tenantID, transferID); err != nil {
		h.log.Error("ship transfer failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "SHIP_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "in_transit"})
}

// ReceiveTransfer handles POST /v1/{tenant}/inventory/transfers/{transferId}/receive
func (h *TransferHandler) ReceiveTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	transferID, err := uuid.Parse(chi.URLParam(r, "transferId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid transfer ID")
		return
	}

	if err := h.transferSvc.ReceiveTransfer(r.Context(), tenantID, transferID); err != nil {
		h.log.Error("receive transfer failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "RECEIVE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

// CancelTransfer handles POST /v1/{tenant}/inventory/transfers/{transferId}/cancel
func (h *TransferHandler) CancelTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	transferID, err := uuid.Parse(chi.URLParam(r, "transferId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid transfer ID")
		return
	}

	if err := h.transferSvc.CancelTransfer(r.Context(), tenantID, transferID); err != nil {
		h.log.Error("cancel transfer failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "CANCEL_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
