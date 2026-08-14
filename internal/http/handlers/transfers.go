package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"

	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
)

// TransferServicer defines the contract for transfer operations.
type TransferServicer interface {
	CreateTransfer(ctx context.Context, tenantID uuid.UUID, req transfers.CreateTransferRequest) (*transfers.TransferResponse, error)
	UpdateTransfer(ctx context.Context, tenantID, transferID uuid.UUID, req transfers.UpdateTransferRequest) (*transfers.TransferResponse, error)
	ListTransfers(ctx context.Context, tenantID uuid.UUID, filter transfers.TransferListFilter) ([]transfers.TransferSummary, int, error)
	GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*transfers.TransferResponse, error)
	ShipTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error
	ReceiveTransfer(ctx context.Context, tenantID, transferID uuid.UUID, req transfers.ReceiveTransferRequest) error
	CancelTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error
}

// TransferHandler handles stock transfer HTTP endpoints.
type TransferHandler struct {
	log         *zap.Logger
	transferSvc TransferServicer
	rbacSvc     *rbac.Service
	appSvc      *approvals.Service
	docSvc      *documents.Service
}

// NewTransferHandler creates a new transfer handler.
func NewTransferHandler(log *zap.Logger, transferSvc TransferServicer, rbacSvc *rbac.Service, appSvc *approvals.Service) *TransferHandler {
	return &TransferHandler{
		log:         log.Named("transfer.handler"),
		transferSvc: transferSvc,
		rbacSvc:     rbacSvc,
		appSvc:      appSvc,
	}
}

// SetDocService wires the tenant-branding-aware document renderer used by GenerateDocument
// (Dispatch/Transit Note + Goods-Received Note PDFs). Optional — GenerateDocument 503s until set.
func (h *TransferHandler) SetDocService(svc *documents.Service) {
	h.docSvc = svc
}

// RegisterRoutes wires transfer routes onto the given chi.Router.
// Transfers move stock between warehouses, so mutations require the stock-change permission.
func (h *TransferHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}
	// Inter-warehouse transfers are tier-gated (stock_transfers is core tier 1 on the
	// use-case PowerSuite families, but standalone/legacy plans may lack it).
	featGate := authclient.RequireFeatureCode("stock_transfers")
	r.Route("/inventory/transfers", func(tr chi.Router) {
		tr.With(featGate, perm(rbac.PermStockChange)).Post("/", h.CreateTransfer)
		tr.Get("/", h.ListTransfers)
		tr.Get("/{transferId}", h.GetTransfer)
		tr.Get("/{transferId}/pdf", h.GenerateDocument)
		tr.With(featGate, perm(rbac.PermStockChange)).Put("/{transferId}", h.UpdateTransfer)
		tr.With(featGate, perm(rbac.PermStockChange)).Post("/{transferId}/ship", h.ShipTransfer)
		tr.With(featGate, perm(rbac.PermStockChange)).Post("/{transferId}/receive", h.ReceiveTransfer)
		tr.With(featGate, perm(rbac.PermStockChange)).Post("/{transferId}/cancel", h.CancelTransfer)
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

// UpdateTransfer handles PUT /v1/{tenant}/inventory/transfers/{transferId} — amends a DRAFT
// transfer's line items and header fields before it ships (the service itself enforces the
// draft-only guard; a 400 surfaces here for any other status).
func (h *TransferHandler) UpdateTransfer(w http.ResponseWriter, r *http.Request) {
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

	var req transfers.UpdateTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "at least one item is required")
		return
	}

	// A prior Ship attempt may have auto-submitted an approval request for this transfer —
	// amending the lines underneath a request that's already pending sign-off would leave the
	// approver reviewing stale quantities, so block until it's resolved (reused, not
	// duplicated: same Satisfied check ShipTransfer's gate uses).
	if h.appSvc != nil {
		if ok, state, aerr := h.appSvc.Satisfied(r.Context(), tenantID, "stock_transfer", transferID, 0); aerr == nil && !ok && state == "pending" {
			writeError(w, http.StatusConflict, "APPROVAL_PENDING", "This transfer is awaiting approval sign-off — resolve it before amending.")
			return
		}
	}

	resp, err := h.transferSvc.UpdateTransfer(r.Context(), tenantID, transferID, req)
	if err != nil {
		h.log.Error("update transfer failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
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
	if from, to, ok := parseCreatedAtRange(r); ok {
		filter.From, filter.To = from, to
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

	// Approval gate: shipping moves stock between warehouses. Gate when a
	// stock_transfer rule exists (no natural amount → 0). Reuses the shared engine.
	if h.appSvc != nil {
		if ok, state, aerr := h.appSvc.Satisfied(r.Context(), tenantID, "stock_transfer", transferID, 0); aerr == nil && !ok {
			if state == "not_submitted" {
				var by *uuid.UUID
				if uid, ok := actingUserID(r); ok {
					by = &uid
				}
				_, _, _ = h.appSvc.Submit(r.Context(), tenantID, "stock_transfer", transferID, "", 0, by)
				writeError(w, http.StatusConflict, "APPROVAL_REQUIRED", "This transfer requires approval — it has been submitted for sign-off.")
			} else if state == "pending" {
				writeError(w, http.StatusConflict, "APPROVAL_PENDING", "This transfer is awaiting approval sign-off.")
			} else {
				writeError(w, http.StatusConflict, "APPROVAL_REJECTED", "The approval request for this transfer was rejected.")
			}
			return
		}
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

	// Body is optional — an empty/absent body means "everything arrived as shipped" (the
	// original, still-default behavior). Only decode when the caller actually sent one.
	var req transfers.ReceiveTransferRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
			return
		}
	}

	if err := h.transferSvc.ReceiveTransfer(r.Context(), tenantID, transferID, req); err != nil {
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
