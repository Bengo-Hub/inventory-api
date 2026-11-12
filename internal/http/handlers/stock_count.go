package handlers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entbalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	entcountline "github.com/bengobox/inventory-service/internal/ent/stockcountline"
	enttemplate "github.com/bengobox/inventory-service/internal/ent/stockcounttemplate"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// countableTypes are the item types that physically hold stock and therefore
// belong on a count sheet. RECIPE/SERVICE/VOUCHER items never track their own
// stock (a recipe's INGREDIENTS are what gets counted), so they are excluded
// from snapshot/template seeding and rejected on manual/scan add.
var countableTypes = []entitem.Type{entitem.TypeGOODS, entitem.TypeINGREDIENT, entitem.TypeEQUIPMENT}

// StockCountHandler manages cycle / physical stock counts and posts approved
// variance adjustments (reusing stock.Service.AdjustStock).
type StockCountHandler struct {
	log         *zap.Logger
	orm         *ent.Client
	stockSvc    *stock.Service
	rbacSvc     *rbac.Service
	auditSvc    *audit.Service
	approvalSvc *approvals.Service
	docSvc      *documents.Service
}

// NewStockCountHandler constructs a stock-count handler.
func NewStockCountHandler(log *zap.Logger, orm *ent.Client, stockSvc *stock.Service, rbacSvc *rbac.Service, auditSvc *audit.Service) *StockCountHandler {
	return &StockCountHandler{log: log.Named("stock_count.handler"), orm: orm, stockSvc: stockSvc, rbacSvc: rbacSvc, auditSvc: auditSvc}
}

// SetApprovalService wires the approval engine so a configured stock_count ApprovalRule gates the
// variance-posting Approve step. Optional — with no service (or no matching rule) the count's own
// RBAC-gated review→approve lifecycle is the only control (auto-approve by default).
func (h *StockCountHandler) SetApprovalService(a *approvals.Service) { h.approvalSvc = a }

// SetDocService wires tenant branding + document numbering: branded count-sheet/variance-report
// PDFs, and a minted reference for a count created without one. Optional — with no service the
// count still works, it just carries whatever reference the caller supplied and cannot render.
func (h *StockCountHandler) SetDocService(d *documents.Service) { h.docSvc = d }

// RegisterRoutes wires stock-count routes onto the inventory group.
func (h *StockCountHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}
	// Stock take is tier-gated (use-case PowerSuite: hosp/duka tier 2+, dawa tier 3).
	featGate := authclient.RequireFeatureCode("stock_take")
	r.Route("/inventory/stock-counts", func(sc chi.Router) {
		sc.Get("/", h.List)
		sc.With(featGate, perm(rbac.PermStockCountAdd)).Post("/", h.Create)
		sc.Get("/{id}", h.Get)
		sc.Get("/{id}/pdf", h.GenerateStockCountPDF)
		sc.With(featGate, perm(rbac.PermStockCountChange)).Post("/{id}/lines", h.UpsertLine)
		sc.With(featGate, perm(rbac.PermStockCountChange)).Post("/{id}/submit", h.Submit)
		sc.With(featGate, perm(rbac.PermStockCountApprove)).Post("/{id}/approve", h.Approve)
		sc.With(featGate, perm(rbac.PermStockCountChange)).Post("/{id}/cancel", h.Cancel)
	})
	// Reusable department count sheets (kitchen daily sheet, barista counter, …).
	// Configuring sheets is a supervisor task (change perm); anyone who can count can list them.
	r.Route("/inventory/stock-count-templates", func(tc chi.Router) {
		tc.Get("/", h.ListTemplates)
		tc.With(featGate, perm(rbac.PermStockCountChange)).Post("/", h.CreateTemplate)
		tc.With(featGate, perm(rbac.PermStockCountChange)).Put("/{id}", h.UpdateTemplate)
		tc.With(featGate, perm(rbac.PermStockCountChange)).Delete("/{id}", h.DeleteTemplate)
	})
}

type createCountRequest struct {
	WarehouseID uuid.UUID `json:"warehouse_id"`
	Reference   string    `json:"reference,omitempty"`
	Snapshot    bool      `json:"snapshot"` // pre-populate lines from current balances
	// TemplateID starts the count from a department sheet: only the template's items
	// (explicit + category members) are pre-populated, each at its current system
	// on-hand — the "expected closing stock". Overrides Snapshot.
	TemplateID *uuid.UUID `json:"template_id,omitempty"`
}

// Create handles POST /inventory/stock-counts.
func (h *StockCountHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req createCountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	ctx := r.Context()

	// Template-driven count: the department sheet supplies the item set and (when the
	// request doesn't name one) the location; the reference defaults to the sheet name.
	var tpl *ent.StockCountTemplate
	if req.TemplateID != nil {
		var tErr error
		tpl, tErr = h.orm.StockCountTemplate.Query().
			Where(enttemplate.ID(*req.TemplateID), enttemplate.TenantID(tenantID)).
			Only(ctx)
		if tErr != nil {
			writeError(w, http.StatusNotFound, "TEMPLATE_NOT_FOUND", "Count sheet template not found")
			return
		}
		if req.WarehouseID == uuid.Nil && tpl.WarehouseID != nil {
			req.WarehouseID = *tpl.WarehouseID
		}
		if req.Reference == "" {
			req.Reference = tpl.Name + " · " + time.Now().Format("2006-01-02 15:04")
		}
	}
	// No explicit warehouse (and none from a template) → default to the operating outlet's own
	// warehouse so the count — and the adjustments its approval posts — land where that outlet's
	// POS terminal reads stock, not on the tenant default warehouse.
	if req.WarehouseID == uuid.Nil {
		if oid := operatingOutletID(r); oid != uuid.Nil {
			if wh, e := h.orm.Warehouse.Query().
				Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletIDEQ(oid), entwarehouse.IsActive(true)).
				First(ctx); e == nil {
				req.WarehouseID = wh.ID
			}
		}
	}
	if req.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "warehouse_id is required")
		return
	}
	// A count raised without a human reference ("CYCLE-2026-06", a sheet name) gets a minted
	// document number so its printed sheet and variance report have a stable identifier. Never
	// overwrites a reference the caller (or a template) already supplied; best-effort, since a
	// numbering failure must not block the count.
	if strings.TrimSpace(req.Reference) == "" && h.docSvc != nil {
		if n, e := h.docSvc.Seq().GenerateNumber(ctx, tenantID, documents.DocTypeStockCount); e == nil && n != "" {
			req.Reference = n
		}
	}

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

	switch {
	case tpl != nil:
		// Seed exactly the sheet's items: explicit item_ids plus every active item in
		// its categories, each snapshotted at the warehouse's current on-hand (0 when
		// no balance row — the sheet still lists it so staff record the physical count).
		idSet := map[uuid.UUID]struct{}{}
		for _, id := range tpl.ItemIds {
			idSet[id] = struct{}{}
		}
		if len(tpl.CategoryIds) > 0 {
			catItems, _ := h.orm.Item.Query().
				Where(
					entitem.TenantID(tenantID),
					entitem.IsActive(true),
					entitem.CategoryIDIn(tpl.CategoryIds...),
					entitem.TypeIn(countableTypes...),
				).
				IDs(ctx)
			for _, id := range catItems {
				idSet[id] = struct{}{}
			}
		}
		ids := make([]uuid.UUID, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			// TypeIn also sanitizes explicit item_ids — a sheet that references a
			// RECIPE/SERVICE item (or an item later converted to one) never seeds it.
			itms, _ := h.orm.Item.Query().
				Where(entitem.TenantID(tenantID), entitem.IDIn(ids...), entitem.TypeIn(countableTypes...)).
				All(ctx)
			onHand := map[uuid.UUID]float64{}
			bals, _ := h.orm.InventoryBalance.Query().
				Where(
					entbalance.TenantID(tenantID),
					entbalance.WarehouseID(req.WarehouseID),
					entbalance.ItemIDIn(ids...),
				).
				All(ctx)
			for _, bal := range bals {
				onHand[bal.ItemID] = bal.OnHand
			}
			for _, it := range itms {
				_, _ = h.orm.StockCountLine.Create().
					SetTenantID(tenantID).
					SetStockCountID(count.ID).
					SetItemID(it.ID).
					SetSku(it.Sku).
					SetSystemQty(onHand[it.ID]).
					Save(ctx)
			}
		}
	case req.Snapshot:
		balances, _ := h.orm.InventoryBalance.Query().
			Where(entbalance.TenantID(tenantID), entbalance.WarehouseID(req.WarehouseID)).
			All(ctx)
		itemIDs := make([]uuid.UUID, 0, len(balances))
		for _, bal := range balances {
			itemIDs = append(itemIDs, bal.ItemID)
		}
		// Only stock-tracked types are countable — legacy/phantom balance rows for
		// RECIPE items (menu entries hold no stock of their own) never seed a line.
		skuByID := make(map[uuid.UUID]string, len(itemIDs))
		if len(itemIDs) > 0 {
			itms, _ := h.orm.Item.Query().
				Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...), entitem.TypeIn(countableTypes...)).
				All(ctx)
			for _, it := range itms {
				skuByID[it.ID] = it.Sku
			}
		}
		for _, bal := range balances {
			if _, countable := skuByID[bal.ItemID]; !countable {
				continue
			}
			_, _ = h.orm.StockCountLine.Create().
				SetTenantID(tenantID).
				SetStockCountID(count.ID).
				SetItemID(bal.ItemID).
				SetSku(skuByID[bal.ItemID]).
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
	// Reason classifies the variance for posting (wastage → damaged, pilferage →
	// shrinkage, surplus → found, …). Empty keeps/clears to plain count_variance.
	Reason *string `json:"reason,omitempty"`
}

// varianceReasons are the StockAdjustment reasons a count line's variance may be
// classified as during review. count_variance is the default when unclassified.
var varianceReasons = map[string]bool{
	"damaged": true, "expired": true, "shrinkage": true, "found": true,
	"correction": true, "count_variance": true, "other": true,
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
	count, err := h.orm.StockCount.Query().
		Where(entcount.ID(countID), entcount.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Stock count not found")
		return
	}
	// Editable through counting AND review (client-requested: staff must be able to fix a
	// mis-entered quantity while a count sits pending approval, not just before it's submitted —
	// see the "editing button before approval" ask). Once approved, variance has already been
	// posted as real stock adjustments — editing the line after that would silently desync the
	// posted adjustment from what the line now shows, so it's blocked; same for cancelled.
	if count.Status == entcount.StatusApproved || count.Status == entcount.StatusCancelled {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS",
			"This stock count has already been "+strings.ToLower(string(count.Status))+" and can no longer be edited")
		return
	}
	// Countable types only: a RECIPE/SERVICE/VOUCHER item holds no stock of its own,
	// so counting it is meaningless (its INGREDIENTS are what gets counted). Reject
	// with guidance instead of silently recording an uncorrectable variance.
	itm, itemErr := h.orm.Item.Query().
		Where(entitem.ID(req.ItemID), entitem.TenantID(tenantID)).
		Only(ctx)
	if itemErr != nil {
		writeError(w, http.StatusNotFound, "ITEM_NOT_FOUND", "Item not found")
		return
	}
	countable := false
	for _, t := range countableTypes {
		if itm.Type == t {
			countable = true
			break
		}
	}
	if !countable {
		writeError(w, http.StatusBadRequest, "NOT_COUNTABLE",
			itm.Name+" is a "+strings.ToLower(string(itm.Type))+" item — it doesn't hold stock of its own, so it can't be counted. Count its ingredients/goods instead.")
		return
	}

	existing, _ := h.orm.StockCountLine.Query().
		Where(entcountline.StockCountID(countID), entcountline.ItemID(req.ItemID)).
		Only(ctx)

	sysQty := 0.0
	if req.SystemQty != nil {
		sysQty = *req.SystemQty
	} else if existing != nil {
		sysQty = existing.SystemQty
	} else {
		// New line without an explicit snapshot qty (scan-added mid-count): snapshot the
		// warehouse balance now so the variance is measured against something real.
		if bal, bErr := h.orm.InventoryBalance.Query().
			Where(
				entbalance.TenantID(tenantID),
				entbalance.WarehouseID(count.WarehouseID),
				entbalance.ItemID(req.ItemID),
			).
			First(ctx); bErr == nil {
			sysQty = bal.OnHand
		}
	}
	// Counts support fractional units (kg/L) — keep 4 dp, matching balance precision.
	round4 := func(v float64) float64 { return math.Round(v*10000) / 10000 }
	countedQty := round4(req.CountedQty)
	variance := round4(countedQty - sysQty)

	reason := ""
	if req.Reason != nil {
		reason = strings.ToLower(strings.TrimSpace(*req.Reason))
		if reason != "" && !varianceReasons[reason] {
			writeError(w, http.StatusBadRequest, "INVALID_REASON", "reason must be one of: damaged, expired, shrinkage, found, correction, count_variance, other")
			return
		}
	}

	// A posted line already moved stock: changing it would desync the line from the adjustment that
	// was posted, so it is refused (the unposted lines of a partly posted count stay editable).
	if existing != nil && existing.Posted {
		writeError(w, http.StatusConflict, "LINE_POSTED", "This line's variance has already been posted and can no longer be edited")
		return
	}
	changed := existing == nil || existing.CountedQty == nil || *existing.CountedQty != countedQty ||
		existing.Variance == nil || *existing.Variance != variance ||
		(req.Reason != nil && reason != existing.Reason)
	if !changed {
		// Re-saving the same numbers is not a change: nothing is written and the approval stays.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "variance": variance})
		return
	}
	if aerr := h.saveCountLineLocked(ctx, tenantID, countID, existing, upsertLineWrite{
		itemID: req.ItemID, sku: req.Sku, sysQty: sysQty, countedQty: countedQty, variance: variance,
		reason: reason, setReason: req.Reason != nil,
	}); aerr != nil {
		writeActionError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "variance": variance})
}

// upsertLineWrite is the line change UpsertLine saves.
type upsertLineWrite struct {
	itemID     uuid.UUID
	sku        string
	sysQty     float64
	countedQty float64
	variance   float64
	reason     string
	setReason  bool
}

// saveCountLineLocked saves a count line change and withdraws the count's live approval in ONE
// transaction that first locks the count's approval request rows (SELECT ... FOR UPDATE).
// ClaimExecution updates those rows, so a claim that starts while the line is being changed waits
// for this transaction and then finds the request cancelled (it only claims approved requests):
// the old approval can never post the new numbers. A request that was already claimed and is
// being posted (approved, executed_at set) freezes the lines: 409 COUNT_POSTING.
func (h *StockCountHandler) saveCountLineLocked(ctx context.Context, tenantID, countID uuid.UUID, existing *ent.StockCountLine, lw upsertLineWrite) *actionError {
	saveFailed := &actionError{Status: http.StatusInternalServerError, Code: "SAVE_FAILED", Message: "The count line was not saved. Try again."}
	tx, err := h.orm.Tx(ctx)
	if err != nil {
		return saveFailed
	}
	fail := func(e *actionError) *actionError {
		_ = tx.Rollback()
		return e
	}
	// Lock every request of this count. ent is generated without the lock feature, so the lock is
	// added to the select through a predicate on the SQL builder.
	reqs, err := tx.ApprovalRequest.Query().
		Where(
			areq.TenantID(tenantID), areq.ModuleEQ(areq.ModuleStockCount), areq.ObjectID(countID),
			predicate.ApprovalRequest(func(s *entsql.Selector) { s.ForUpdate() }),
		).
		All(ctx)
	if err != nil {
		return fail(saveFailed)
	}
	live := make([]uuid.UUID, 0, len(reqs))
	for _, rq := range reqs {
		if rq.Status == areq.StatusApproved && rq.ExecutedAt != nil {
			return fail(&actionError{Status: http.StatusConflict, Code: "COUNT_POSTING",
				Message: "This count is being posted on its approval; its lines can no longer be edited"})
		}
		if rq.Status == areq.StatusPending || rq.Status == areq.StatusApproved {
			live = append(live, rq.ID)
		}
	}
	// The live approval (pending, or approved and not run yet) was given for the old numbers, even
	// when the total variance happens to stay the same: withdraw it, so the next Approve opens a
	// fresh request for what the count now says.
	if len(live) > 0 {
		if _, err := tx.ApprovalRequest.Update().
			Where(areq.IDIn(live...), areq.TenantID(tenantID)).
			SetStatus(areq.StatusCancelled).SetDecidedAt(time.Now()).
			Save(ctx); err != nil {
			return fail(saveFailed)
		}
	}
	if existing != nil {
		// Conditional on the line still being unposted, so an edit never overwrites a posted line.
		upd := tx.StockCountLine.Update().
			Where(entcountline.ID(existing.ID), entcountline.TenantID(tenantID), entcountline.Posted(false)).
			SetCountedQty(lw.countedQty).SetVariance(lw.variance)
		if lw.setReason {
			upd = upd.SetReason(lw.reason)
		}
		n, err := upd.Save(ctx)
		if err != nil {
			return fail(saveFailed)
		}
		if n == 0 {
			return fail(&actionError{Status: http.StatusConflict, Code: "LINE_POSTED",
				Message: "This line's variance has already been posted and can no longer be edited"})
		}
	} else {
		create := tx.StockCountLine.Create().
			SetTenantID(tenantID).
			SetStockCountID(countID).
			SetItemID(lw.itemID).
			SetSku(lw.sku).
			SetSystemQty(lw.sysQty).
			SetCountedQty(lw.countedQty).
			SetVariance(lw.variance)
		if lw.reason != "" {
			create = create.SetReason(lw.reason)
		}
		if _, err := create.Save(ctx); err != nil {
			return fail(saveFailed)
		}
	}
	if err := tx.Commit(); err != nil {
		return saveFailed
	}
	return nil
}

// countGateAmount is a stock count's approval amount: the total absolute variance of all its
// counted lines, posted ones included, summed in a fixed order and rounded to 4 decimals so two
// computations over the same lines always give the same figure.
func countGateAmount(ctx context.Context, orm *ent.Client, tenantID, countID uuid.UUID) (float64, error) {
	lines, err := orm.StockCountLine.Query().
		Where(entcountline.TenantID(tenantID), entcountline.StockCountID(countID)).
		Order(ent.Asc(entcountline.FieldID)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, ln := range lines {
		if ln.CountedQty == nil || ln.Variance == nil {
			continue
		}
		total += math.Abs(*ln.Variance)
	}
	return math.Round(total*10000) / 10000, nil
}

// errCountChanged is the refusal when a count no longer matches the amount its approval was given
// for (a line changed after sign-off): the approval must not post numbers nobody approved.
var errCountChanged = &actionError{Status: http.StatusConflict, Code: "COUNT_CHANGED",
	Message: "This count changed after it was approved, so it was not posted. Submit it for approval again."}

// Submit handles POST /inventory/stock-counts/{id}/submit — counting → review.
func (h *StockCountHandler) Submit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "review", nil)
}

// Cancel handles POST /inventory/stock-counts/{id}/cancel.
// A cancelled count must never post, so its live approval request (pending, or approved and not
// yet executed) is withdrawn with it.
// A count that already posted (approved, or with any posted line) has moved stock and cannot be
// cancelled: 409 COUNT_POSTED.
func (h *StockCountHandler) Cancel(w http.ResponseWriter, r *http.Request) {
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
	count, err := h.orm.StockCount.Query().Where(entcount.ID(countID), entcount.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return
	}
	posted, err := h.orm.StockCountLine.Query().
		Where(entcountline.TenantID(tenantID), entcountline.StockCountID(countID), entcountline.Posted(true)).
		Exist(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to check the count")
		return
	}
	if count.Status == entcount.StatusApproved || posted {
		writeError(w, http.StatusConflict, "COUNT_POSTED", "This count has already posted stock adjustments and cannot be cancelled")
		return
	}
	if !h.transition(w, r, "cancelled", nil) || h.approvalSvc == nil {
		return
	}
	if _, err := h.approvalSvc.WithdrawLive(r.Context(), tenantID, "stock_count", countID); err != nil {
		h.log.Warn("withdraw approval of cancelled count failed", zap.String("count_id", countID.String()), zap.Error(err))
	}
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

	// The gate amount covers every counted line, posted ones included: the approval covers the
	// whole count, so after a partial post (some SKUs failed) the amount stays the same and
	// pressing Approve again finishes the remaining lines on the ORIGINAL approval. Changing a
	// count line withdraws the approval (UpsertLine), so the next press is a new submission.
	gateAmount, lerr := countGateAmount(ctx, h.orm, tenantID, countID)
	if lerr != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load count lines")
		return
	}

	// Approval-engine gate: a configured stock_count ApprovalRule routes the variance posting
	// through the workflow, keyed on the count itself (objectID = countID). With no matching rule
	// the count's own RBAC review to approve step is authoritative (auto-approve, the default), so
	// a tenant that has not configured a rule is never blocked. The gate amount is the total
	// absolute variance of the count (net offsets don't mask a large exposure).
	var approvedReq *ent.ApprovalRequest
	if h.approvalSvc != nil {
		// A rejected count is corrected (lines stay editable) and Approve pressed again: that opens
		// a new request; the rejected one stays as history. Approving the request posts the count
		// through the approval executor.
		res, gerr := h.approvalSvc.Gate(ctx, tenantID, "stock_count", countID, "stock-count:"+countID.String(), gateAmount, &approver, nil)
		if gerr != nil {
			// Fail closed: variances never post unapproved because of a database fault.
			h.log.Error("stock count approval gate failed; approve refused", zap.Error(gerr))
			writeApprovalUnavailable(w)
			return
		} else if !res.Proceed() {
			body := map[string]any{
				"approval_required": true, "module": "stock_count", "object_id": countID, "state": res.State,
			}
			if res.State == approvals.StatePending {
				body["error"] = "STOCK_COUNT_APPROVAL_PENDING"
			}
			if res.Request != nil {
				body["request_id"] = res.Request.ID
			}
			writeJSON(w, http.StatusUnprocessableEntity, body)
			return
		} else if res.State == approvals.StateApproved {
			approvedReq = res.Request
		}
	}
	// The approval (when there is one) is claimed before posting, so this press and the approval
	// executor never both post.
	if approvedReq != nil {
		won, cerr := h.approvalSvc.ClaimExecution(ctx, tenantID, approvedReq.ID)
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "CLAIM_FAILED", "Could not record the approval as used. Try again.")
			return
		}
		if !won {
			writeError(w, http.StatusConflict, "ALREADY_EXECUTED", "This count is already being posted on its approval.")
			return
		}
		// Second guard, after the claim: the count must still be what was approved.
		if now, aerr := countGateAmount(ctx, h.orm, tenantID, countID); aerr != nil || now != approvedReq.Amount {
			_ = h.approvalSvc.ReleaseExecution(ctx, tenantID, approvedReq.ID)
			writeActionError(w, errCountChanged)
			return
		}
	}
	// Post every unposted variance line (and finalize the count when all post). Shared with
	// the approval-execution path so a matrix-approved count posts identically — the Posted
	// flag keeps it idempotent across the RBAC approve and the approval-matrix sign-off.
	result, err := h.stockSvc.PostStockCountVariances(ctx, tenantID, countID, approver)
	if err != nil {
		if approvedReq != nil {
			_ = h.approvalSvc.ReleaseExecution(ctx, tenantID, approvedReq.ID)
		}
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to post count variances")
		return
	}
	if approvedReq != nil && len(result.FailedSKUs) > 0 {
		// Some lines did not post: keep the approval usable for the retry.
		_ = h.approvalSvc.ReleaseExecution(ctx, tenantID, approvedReq.ID)
	}
	posted, totalVar := result.Posted, result.TotalVariance

	// All-or-nothing: the count is marked approved only once EVERY variance line has posted. Posted
	// lines are flagged Posted(true), so re-invoking Approve is idempotent (it skips them) — the
	// operator fixes the failed SKUs and re-approves to post the remainder, instead of the count
	// being marked approved with some variances silently dropped.
	if len(result.FailedSKUs) > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":         "partial",
			"posted_lines":   posted,
			"failed_skus":    result.FailedSKUs,
			"total_variance": totalVar,
			"message":        "Some variance lines could not post; resolve them and approve again to finish.",
		})
		return
	}

	updated, err := h.orm.StockCount.Query().Where(entcount.ID(countID), entcount.TenantID(tenantID)).Only(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to load approved count")
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

func (h *StockCountHandler) transition(w http.ResponseWriter, r *http.Request, status string, _ any) bool {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return false
	}
	countID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid count id")
		return false
	}
	n, err := h.orm.StockCount.Update().
		Where(entcount.ID(countID), entcount.TenantID(tenantID)).
		SetStatus(entcount.Status(status)).
		Save(r.Context())
	if err != nil || n == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count not found")
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
	return true
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
	countIDs := make([]uuid.UUID, 0, len(rows))
	for _, c := range rows {
		countIDs = append(countIDs, c.ID)
	}
	summaries := h.varianceSummaryByCount(r.Context(), countIDs)

	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		dto := countDTO(c)
		if vc, ok := summaries[c.ID]; ok {
			dto["pending_lines"] = vc.pending
			dto["positive_lines"] = vc.positive
			dto["negative_lines"] = vc.negative
			dto["matched_lines"] = vc.matched
			dto["total_lines"] = vc.pending + vc.positive + vc.negative + vc.matched
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// varianceCounts is a per-stock-count line-status breakdown — pending (not yet counted), positive
// (surplus), negative (shortage), matched (counted, exact match). Powers the Stock Take list
// page's Variance column and the detail page's pending/positive/negative filter pills.
type varianceCounts struct{ pending, positive, negative, matched int }

// varianceSummaryByCount computes varianceCounts for every id in countIDs in ONE query (only the
// 3 columns needed, never the full line rows) — avoids an N+1 per row on the list page. Best-effort:
// a query failure returns an empty map, so the list still renders (just without the summary),
// matching this handler's existing fail-soft style elsewhere (e.g. item enrichment in Get).
func (h *StockCountHandler) varianceSummaryByCount(ctx context.Context, countIDs []uuid.UUID) map[uuid.UUID]varianceCounts {
	out := map[uuid.UUID]varianceCounts{}
	if len(countIDs) == 0 {
		return out
	}
	lines, err := h.orm.StockCountLine.Query().
		Where(entcountline.StockCountIDIn(countIDs...)).
		Select(entcountline.FieldStockCountID, entcountline.FieldCountedQty, entcountline.FieldVariance).
		All(ctx)
	if err != nil {
		h.log.Warn("variance summary query failed — list will render without it", zap.Error(err))
		return out
	}
	for _, ln := range lines {
		vc := out[ln.StockCountID]
		switch {
		case ln.CountedQty == nil:
			vc.pending++
		case ln.Variance != nil && *ln.Variance > 0:
			vc.positive++
		case ln.Variance != nil && *ln.Variance < 0:
			vc.negative++
		default:
			vc.matched++
		}
		out[ln.StockCountID] = vc
	}
	return out
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

	// Enrich lines with the item's name, barcode and stock unit so the count sheet
	// reads naturally ("Sugar · INGR-SUGAR · kg") and hardware/camera scans can be
	// matched client-side against barcode OR sku without extra lookups.
	itemIDs := make([]uuid.UUID, 0, len(lines))
	for _, ln := range lines {
		itemIDs = append(itemIDs, ln.ItemID)
	}
	type itemInfo struct {
		name, barcode, unit, itemType, categoryName string
	}
	infoByID := make(map[uuid.UUID]itemInfo, len(itemIDs))
	if len(itemIDs) > 0 {
		itms, _ := h.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).
			WithUnits().
			WithItemCategory().
			All(ctx)
		for _, it := range itms {
			info := itemInfo{name: it.Name, barcode: it.Barcode, itemType: string(it.Type)}
			if it.Edges.Units != nil {
				info.unit = it.Edges.Units.Abbreviation
			}
			if it.Edges.ItemCategory != nil {
				info.categoryName = it.Edges.ItemCategory.Name
			}
			infoByID[it.ID] = info
		}
	}

	lineOut := make([]map[string]any, 0, len(lines))
	for _, ln := range lines {
		info := infoByID[ln.ItemID]
		lineOut = append(lineOut, map[string]any{
			"id": ln.ID, "item_id": ln.ItemID, "sku": ln.Sku,
			"item_name": info.name, "barcode": info.barcode, "unit": info.unit,
			"item_type": info.itemType, "category_name": info.categoryName,
			"system_qty": ln.SystemQty, "counted_qty": ln.CountedQty, "variance": ln.Variance,
			"reason": ln.Reason, "posted": ln.Posted,
		})
	}
	// Stable, human order: by item name (falling back to SKU) so the sheet matches how
	// shelves are walked, not insertion order.
	sort.Slice(lineOut, func(i, j int) bool {
		ni, _ := lineOut[i]["item_name"].(string)
		nj, _ := lineOut[j]["item_name"].(string)
		if ni == "" {
			ni, _ = lineOut[i]["sku"].(string)
		}
		if nj == "" {
			nj, _ = lineOut[j]["sku"].(string)
		}
		return strings.ToLower(ni) < strings.ToLower(nj)
	})
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

// ── Count-sheet templates (department stock sheets) ─────────────────────────────

type templateRequest struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	WarehouseID *uuid.UUID  `json:"warehouse_id,omitempty"`
	ItemIDs     []uuid.UUID `json:"item_ids,omitempty"`
	CategoryIDs []uuid.UUID `json:"category_ids,omitempty"`
	IsActive    *bool       `json:"is_active,omitempty"`
}

func templateDTO(t *ent.StockCountTemplate) map[string]any {
	return map[string]any{
		"id": t.ID, "name": t.Name, "description": t.Description,
		"warehouse_id": t.WarehouseID, "item_ids": t.ItemIds, "category_ids": t.CategoryIds,
		"is_active": t.IsActive, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
	}
}

// ListTemplates handles GET /inventory/stock-count-templates.
func (h *StockCountHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	rows, err := h.orm.StockCountTemplate.Query().
		Where(enttemplate.TenantID(tenantID)).
		Order(ent.Asc(enttemplate.FieldName)).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to list count sheets")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, templateDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// CreateTemplate handles POST /inventory/stock-count-templates.
func (h *StockCountHandler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req templateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "name is required")
		return
	}
	if len(req.ItemIDs) == 0 && len(req.CategoryIDs) == 0 {
		writeError(w, http.StatusBadRequest, "EMPTY_SHEET", "Pick at least one item or category for the sheet")
		return
	}
	b := h.orm.StockCountTemplate.Create().
		SetTenantID(tenantID).
		SetName(strings.TrimSpace(req.Name)).
		SetDescription(req.Description).
		SetItemIds(req.ItemIDs).
		SetCategoryIds(req.CategoryIDs).
		SetNillableWarehouseID(req.WarehouseID)
	t, err := b.Save(r.Context())
	if err != nil {
		if ent.IsConstraintError(err) {
			writeError(w, http.StatusConflict, "DUPLICATE_NAME", "A count sheet with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to create count sheet")
		return
	}
	writeJSON(w, http.StatusCreated, templateDTO(t))
}

// UpdateTemplate handles PUT /inventory/stock-count-templates/{id}.
func (h *StockCountHandler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid template id")
		return
	}
	var req templateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	ctx := r.Context()
	t, err := h.orm.StockCountTemplate.Query().
		Where(enttemplate.ID(id), enttemplate.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count sheet not found")
		return
	}
	upd := t.Update()
	if strings.TrimSpace(req.Name) != "" {
		upd = upd.SetName(strings.TrimSpace(req.Name))
	}
	upd = upd.SetDescription(req.Description)
	if req.ItemIDs != nil {
		upd = upd.SetItemIds(req.ItemIDs)
	}
	if req.CategoryIDs != nil {
		upd = upd.SetCategoryIds(req.CategoryIDs)
	}
	if req.WarehouseID != nil {
		if *req.WarehouseID == uuid.Nil {
			upd = upd.ClearWarehouseID()
		} else {
			upd = upd.SetWarehouseID(*req.WarehouseID)
		}
	}
	if req.IsActive != nil {
		upd = upd.SetIsActive(*req.IsActive)
	}
	updated, err := upd.Save(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "Failed to update count sheet")
		return
	}
	writeJSON(w, http.StatusOK, templateDTO(updated))
}

// DeleteTemplate handles DELETE /inventory/stock-count-templates/{id}.
func (h *StockCountHandler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid template id")
		return
	}
	n, err := h.orm.StockCountTemplate.Delete().
		Where(enttemplate.ID(id), enttemplate.TenantID(tenantID)).
		Exec(r.Context())
	if err != nil || n == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Count sheet not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
