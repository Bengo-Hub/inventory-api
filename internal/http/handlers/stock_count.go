package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entbalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	entcountline "github.com/bengobox/inventory-service/internal/ent/stockcountline"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// StockCountHandler manages cycle / physical stock counts and posts approved
// variance adjustments (reusing stock.Service.AdjustStock).
type StockCountHandler struct {
	log      *zap.Logger
	orm      *ent.Client
	stockSvc *stock.Service
	rbacSvc  *rbac.Service
	auditSvc *audit.Service
}

// NewStockCountHandler constructs a stock-count handler.
func NewStockCountHandler(log *zap.Logger, orm *ent.Client, stockSvc *stock.Service, rbacSvc *rbac.Service, auditSvc *audit.Service) *StockCountHandler {
	return &StockCountHandler{log: log.Named("stock_count.handler"), orm: orm, stockSvc: stockSvc, rbacSvc: rbacSvc, auditSvc: auditSvc}
}

// RegisterRoutes wires stock-count routes onto the inventory group.
func (h *StockCountHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}
	r.Route("/inventory/stock-counts", func(sc chi.Router) {
		sc.Get("/", h.List)
		sc.With(perm(rbac.PermStockCountAdd)).Post("/", h.Create)
		sc.Get("/{id}", h.Get)
		sc.With(perm(rbac.PermStockCountChange)).Post("/{id}/lines", h.UpsertLine)
		sc.With(perm(rbac.PermStockCountChange)).Post("/{id}/submit", h.Submit)
		sc.With(perm(rbac.PermStockCountApprove)).Post("/{id}/approve", h.Approve)
		sc.With(perm(rbac.PermStockCountChange)).Post("/{id}/cancel", h.Cancel)
	})
}

type createCountRequest struct {
	WarehouseID uuid.UUID `json:"warehouse_id"`
	Reference   string    `json:"reference,omitempty"`
	Snapshot    bool      `json:"snapshot"` // pre-populate lines from current balances
}

// Create handles POST /inventory/stock-counts.
func (h *StockCountHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req createCountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "warehouse_id is required")
		return
	}
	ctx := r.Context()
	b := h.orm.StockCount.Create().
		SetTenantID(tenantID).
		SetWarehouseID(req.WarehouseID).
		SetStatus("counting")
	if req.Reference != "" {
		b = b.SetReference(req.Reference)
	}
	if claims, ok := authclient.ClaimsFromContext(ctx); ok {
		if uid, e := claims.UserID(); e == nil {
			b = b.SetCountedBy(uid)
		}
	}
	count, err := b.Save(ctx)
	if err != nil {
		h.log.Error("create stock count failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to create count")
		return
	}

	if req.Snapshot {
		balances, _ := h.orm.InventoryBalance.Query().
			Where(entbalance.TenantID(tenantID), entbalance.WarehouseID(req.WarehouseID)).
			All(ctx)
		for _, bal := range balances {
			sku := ""
			if it, e := h.orm.Item.Query().Where(entitem.ID(bal.ItemID), entitem.TenantID(tenantID)).Only(ctx); e == nil {
				sku = it.Sku
			}
			_, _ = h.orm.StockCountLine.Create().
				SetTenantID(tenantID).
				SetStockCountID(count.ID).
				SetItemID(bal.ItemID).
				SetSku(sku).
				SetSystemQty(bal.OnHand).
				Save(ctx)
		}
	}
	writeJSON(w, http.StatusCreated, countDTO(count))
}

type upsertLineRequest struct {
	ItemID     uuid.UUID `json:"item_id"`
	Sku        string    `json:"sku"`
	SystemQty  *float64  `json:"system_qty,omitempty"`
	CountedQty float64   `json:"counted_qty"`
}

// UpsertLine handles POST /inventory/stock-counts/{id}/lines — record a counted quantity.
func (h *StockCountHandler) UpsertLine(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return
	}
	var req upsertLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ItemID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "item_id is required")
		return
	}
	ctx := r.Context()
	existing, _ := h.orm.StockCountLine.Query().
		Where(entcountline.StockCountID(countID), entcountline.ItemID(req.ItemID)).
		Only(ctx)

	sysQty := 0.0
	if req.SystemQty != nil {
		sysQty = *req.SystemQty
	} else if existing != nil {
		sysQty = existing.SystemQty
	}
	variance := req.CountedQty - sysQty

	if existing != nil {
		_, err = existing.Update().SetCountedQty(req.CountedQty).SetVariance(variance).Save(ctx)
	} else {
		_, err = h.orm.StockCountLine.Create().
			SetTenantID(tenantID).
			SetStockCountID(countID).
			SetItemID(req.ItemID).
			SetSku(req.Sku).
			SetSystemQty(sysQty).
			SetCountedQty(req.CountedQty).
			SetVariance(variance).
			Save(ctx)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to save line")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "variance": variance})
}

// Submit handles POST /inventory/stock-counts/{id}/submit — counting → review.
func (h *StockCountHandler) Submit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "review", nil)
}

// Cancel handles POST /inventory/stock-counts/{id}/cancel.
func (h *StockCountHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "cancelled", nil)
}

// Approve handles POST /inventory/stock-counts/{id}/approve — posts variance
// adjustments for every counted line and marks the count approved.
func (h *StockCountHandler) Approve(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return
	}
	ctx := r.Context()
	count, err := h.orm.StockCount.Query().Where(entcount.ID(countID), entcount.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return
	}
	if count.Status == "approved" || count.Status == "cancelled" {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "Count is already "+string(count.Status))
		return
	}

	var approver uuid.UUID
	if claims, ok := authclient.ClaimsFromContext(ctx); ok {
		approver, _ = claims.UserID()
	}

	lines, _ := h.orm.StockCountLine.Query().
		Where(entcountline.StockCountID(countID), entcountline.Posted(false)).
		All(ctx)
	posted, totalVar := 0, 0.0
	failed := make([]string, 0)
	for _, ln := range lines {
		if ln.CountedQty == nil || ln.Variance == nil || *ln.Variance == 0 {
			continue
		}
		_, adjErr := h.stockSvc.AdjustStock(ctx, tenantID, stock.AdjustStockRequest{
			SKU:         ln.Sku,
			Adjustment:  *ln.Variance,
			Reason:      "count_variance",
			Reference:   "stock-count:" + countID.String(),
			Notes:       "cycle count variance",
			AdjustedBy:  approver,
			WarehouseID: count.WarehouseID,
		})
		if adjErr != nil {
			h.log.Warn("post count variance failed", zap.String("sku", ln.Sku), zap.Error(adjErr))
			failed = append(failed, ln.Sku)
			continue
		}
		_, _ = ln.Update().SetPosted(true).Save(ctx)
		posted++
		totalVar += *ln.Variance
	}

	// All-or-nothing: only mark the count approved once EVERY variance line has posted. Posted lines
	// are flagged Posted(true), so re-invoking Approve is idempotent (it skips them) — the operator
	// fixes the failed SKUs and re-approves to post the remainder, instead of the count being marked
	// approved with some variances silently dropped.
	if len(failed) > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":        "partial",
			"posted_lines":  posted,
			"failed_skus":   failed,
			"total_variance": totalVar,
			"message":       "Some variance lines could not post; resolve them and approve again to finish.",
		})
		return
	}

	updated, err := count.Update().
		SetStatus("approved").
		SetApprovedBy(approver).
		SetApprovedAt(time.Now()).
		Save(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to approve count")
		return
	}

	if h.auditSvc != nil {
		oid := count.WarehouseID
		amt := totalVar
		h.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			OutletID:    &oid,
			ActorUserID: approver,
			ApproverID:  &approver,
			Action:      "stock.count_approved",
			EntityType:  "stock_count",
			EntityID:    countID.String(),
			Amount:      &amt,
			After:       map[string]any{"posted_lines": posted, "total_variance": totalVar},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": countDTO(updated), "posted_lines": posted, "total_variance": totalVar})
}

func (h *StockCountHandler) transition(w http.ResponseWriter, r *http.Request, status string, _ any) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return
	}
	n, err := h.orm.StockCount.Update().
		Where(entcount.ID(countID), entcount.TenantID(tenantID)).
		SetStatus(entcount.Status(status)).
		Save(r.Context())
	if err != nil || n == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

// List handles GET /inventory/stock-counts.
func (h *StockCountHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.StockCount.Query().Where(entcount.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entcount.StatusEQ(entcount.Status(s)))
	}
	rows, err := q.Order(ent.Desc(entcount.FieldCreatedAt)).Limit(200).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to list counts")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, countDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// Get handles GET /inventory/stock-counts/{id} with its lines.
func (h *StockCountHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return
	}
	ctx := r.Context()
	count, err := h.orm.StockCount.Query().Where(entcount.ID(countID), entcount.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return
	}
	lines, _ := h.orm.StockCountLine.Query().Where(entcountline.StockCountID(countID)).All(ctx)
	lineOut := make([]map[string]any, 0, len(lines))
	for _, ln := range lines {
		lineOut = append(lineOut, map[string]any{
			"id": ln.ID, "item_id": ln.ItemID, "sku": ln.Sku,
			"system_qty": ln.SystemQty, "counted_qty": ln.CountedQty, "variance": ln.Variance, "posted": ln.Posted,
		})
	}
	dto := countDTO(count)
	dto["lines"] = lineOut
	writeJSON(w, http.StatusOK, dto)
}

func countDTO(c *ent.StockCount) map[string]any {
	return map[string]any{
		"id": c.ID, "warehouse_id": c.WarehouseID, "reference": c.Reference,
		"status": c.Status, "counted_by": c.CountedBy, "approved_by": c.ApprovedBy,
		"approved_at": c.ApprovedAt, "created_at": c.CreatedAt,
	}
}
