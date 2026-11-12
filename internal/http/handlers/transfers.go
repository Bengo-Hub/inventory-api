package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"

	"github.com/bengobox/inventory-service/internal/ent"
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
		if errors.Is(err, transfers.ErrInvalidTransferDate) {
			writeError(w, http.StatusBadRequest, "INVALID_TRANSFER_DATE", err.Error())
			return
		}
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
	// A rejected transfer is amendable (that is how it gets fixed before the next Ship resubmits it).
	if h.appSvc != nil {
		if open, oerr := h.appSvc.OpenRequest(r.Context(), tenantID, "stock_transfer", transferID); oerr == nil && open != nil && open.Status == "pending" {
			writeError(w, http.StatusConflict, "APPROVAL_PENDING", "This transfer is awaiting approval sign-off. Resolve it before amending.")
			return
		}
	}

	resp, err := h.transferSvc.UpdateTransfer(r.Context(), tenantID, transferID, req)
	if err != nil {
		if errors.Is(err, transfers.ErrInvalidTransferDate) {
			writeError(w, http.StatusBadRequest, "INVALID_TRANSFER_DATE", err.Error())
			return
		}
		h.log.Error("update transfer failed", zap.Error(err))
		writeError(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	// An approval that has not shipped yet signed off the old lines: withdraw it, so the amended
	// quantities are approved again on the next Ship.
	if h.appSvc != nil {
		if _, werr := h.appSvc.WithdrawLive(r.Context(), tenantID, "stock_transfer", transferID); werr != nil {
			h.log.Warn("withdraw transfer approval after amend failed", zap.Error(werr))
		}
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
	// Shared page/limit/offset parsing (same helper ItemStockHistory and the items list use) so a
	// caller can page with either `page`+`limit` or a raw `offset` — previously this endpoint only
	// understood `limit`/`offset` and inventory-ui's Transfers page sent neither, silently capping
	// every load at the 50-row fallback inside Service.ListTransfers with no way to reach page 2.
	p := pagination.Parse(r)
	filter.Limit = p.Limit
	filter.Offset = p.Offset

	items, total, err := h.transferSvc.ListTransfers(r.Context(), tenantID, filter)
	if err != nil {
		h.log.Error("list transfers failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"total":   total,
		"limit":   p.Limit,
		"page":    p.Page,
		"hasMore": p.Offset+len(items) < total,
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

	// Only a draft ships. Checked before the approval gate so pressing Ship on a shipped or
	// cancelled transfer never opens a new approval request.
	cur, err := h.transferSvc.GetTransfer(r.Context(), tenantID, transferID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Transfer not found")
		return
	}
	if cur.Status != "draft" {
		writeError(w, http.StatusBadRequest, "SHIP_FAILED", "transfers: can only ship a draft transfer, current status: "+cur.Status)
		return
	}

	// Approval gate: shipping moves stock between warehouses. Gate when a stock_transfer rule
	// exists (no natural amount, so 0). Approving the request ships the transfer; a rejected
	// transfer stays a draft, is amended, and the next Ship resubmits it.
	var approved *ent.ApprovalRequest
	if h.appSvc != nil {
		var by *uuid.UUID
		if uid, ok := actingUserID(r); ok {
			by = &uid
		}
		res, gerr := h.appSvc.Gate(r.Context(), tenantID, "stock_transfer", transferID, cur.TransferNumber, 0, by, nil)
		switch {
		case gerr != nil:
			// Fail closed: a transfer that may need approval never ships on a database fault.
			h.log.Error("transfer approval gate failed; ship refused", zap.Error(gerr))
			writeApprovalUnavailable(w)
			return
		case !res.Proceed():
			writeApprovalGate(w, "stock_transfer", res)
			return
		case res.State == approvals.StateApproved:
			approved = res.Request
		}
	}

	out, aerr := runClaimed(r.Context(), h.appSvc, approved, shipTransferAction(h.transferSvc, tenantID, transferID))
	if aerr != nil {
		h.log.Error("ship transfer failed", zap.String("code", aerr.Code), zap.String("reason", aerr.Message))
		writeActionError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// shipTransferAction ships a draft transfer. Shared by Ship and the approval executor. The
// service flips draft to in_transit with a conditional update, so the stock leaves the source
// once however many callers race; a transfer that is no longer a draft is settled.
func shipTransferAction(svc TransferServicer, tenantID, transferID uuid.UUID) approvalAction {
	return func(ctx context.Context) (any, *actionError) {
		if err := svc.ShipTransfer(ctx, tenantID, transferID); err != nil {
			return nil, &actionError{Status: http.StatusBadRequest, Code: "SHIP_FAILED", Message: err.Error(),
				Settled: errors.Is(err, transfers.ErrTransferNotDraft)}
		}
		return map[string]string{"status": "in_transit"}, nil
	}
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
