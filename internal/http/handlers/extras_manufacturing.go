package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entbrm "github.com/bengobox/inventory-service/internal/ent/batchrawmaterial"
	entib "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
	entqc "github.com/bengobox/inventory-service/internal/ent/qualitycheck"
	entrmu "github.com/bengobox/inventory-service/internal/ent/rawmaterialusage"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
	entri "github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// matShortage describes a raw-material whose available stock is below the batch's
// required quantity (used by the material-availability pre-check).
type matShortage struct {
	ItemID    uuid.UUID `json:"item_id"`
	ItemSku   string    `json:"item_sku"`
	Required  float64   `json:"required"`
	Available int       `json:"available"`
}

// computeMaterialShortages explodes a recipe to the planned quantity and returns
// any ingredients whose tenant-wide available stock is below what the batch needs.
func (h *InventoryExtrasHandler) computeMaterialShortages(ctx context.Context, tenantID, recipeID uuid.UUID, plannedQty float64) ([]matShortage, error) {
	recipe, err := h.orm.Recipe.Query().Where(entrecipe.ID(recipeID), entrecipe.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	ratio := 1.0
	if recipe.OutputQty > 0 {
		ratio = plannedQty / recipe.OutputQty
	}
	ings, _ := h.orm.RecipeIngredient.Query().Where(entri.RecipeID(recipe.ID)).All(ctx)
	shortages := make([]matShortage, 0)
	for _, ing := range ings {
		required := ing.Quantity * ratio
		var agg []struct {
			Sum int `json:"sum"`
		}
		_ = h.orm.InventoryBalance.Query().
			Where(entib.TenantID(tenantID), entib.ItemID(ing.ItemID)).
			Aggregate(ent.Sum(entib.FieldAvailable)).Scan(ctx, &agg)
		avail := 0
		if len(agg) > 0 {
			avail = agg[0].Sum
		}
		if float64(avail) < required {
			shortages = append(shortages, matShortage{ItemID: ing.ItemID, ItemSku: ing.ItemSku, Required: required, Available: avail})
		}
	}
	return shortages, nil
}

// recordRawMaterialUsage writes an audit-trail row for one raw-material movement
// (production consume, return on cancel, or wastage on QC failure/scrap).
func (h *InventoryExtrasHandler) recordRawMaterialUsage(ctx context.Context, tenantID, batchID uuid.UUID, finishedItemID *uuid.UUID, rawItemID uuid.UUID, rawSKU string, qty, cost float64, txType string) {
	c := h.orm.RawMaterialUsage.Create().
		SetTenantID(tenantID).
		SetProductionBatchID(batchID).
		SetRawItemID(rawItemID).
		SetRawSku(rawSKU).
		SetQuantity(qty).
		SetCost(cost).
		SetTransactionType(entrmu.TransactionType(txType))
	if finishedItemID != nil {
		c = c.SetFinishedItemID(*finishedItemID)
	}
	if _, err := c.Save(ctx); err != nil {
		h.log.Warn("record raw-material usage failed", zap.Error(err))
	}
}

// CheckMaterialAvailability previews whether a recipe can be produced at a given
// quantity, returning any raw-material shortages (powers the UI shortage banner
// before a batch is started).
//
//	@Summary      Preview raw-material availability for a recipe + quantity
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        recipe_id  query     string  true   "Recipe ID"
//	@Param        quantity   query     number  false  "Planned output quantity"
//	@Success      200        {object}  map[string]interface{}
//	@Failure      400        {object}  map[string]string
//	@Failure      404        {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/manufacturing/material-check [get]
func (h *InventoryExtrasHandler) CheckMaterialAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	recipeID, err := uuid.Parse(r.URL.Query().Get("recipe_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "recipe_id is required")
		return
	}
	qty, _ := strconv.ParseFloat(r.URL.Query().Get("quantity"), 64)
	if qty <= 0 {
		qty = 1
	}
	shortages, err := h.computeMaterialShortages(r.Context(), tenantID, recipeID, qty)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Recipe not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": len(shortages) == 0, "shortages": shortages})
}

// ─── Manufacturing: production batches + QC (migrated from ERP manufacturing) ──
// BOM = Recipe; raw-material consumption + finished-goods receipt are applied by
// the stock module via emitted events (inventory.production.started/completed) to
// avoid duplicating stock math here (data-ownership / reuse rule).

type productionBatchPayload struct {
	RecipeID        uuid.UUID  `json:"recipe_id"`
	PlannedQuantity float64    `json:"planned_quantity"`
	ScheduledDate   *time.Time `json:"scheduled_date"`
	OutletID        *uuid.UUID `json:"outlet_id"`
	SupervisorID    *uuid.UUID `json:"supervisor_id"`
	CreatedBy       *uuid.UUID `json:"created_by"`
	LaborCost       float64    `json:"labor_cost"`
	OverheadCost    float64    `json:"overhead_cost"`
	Notes           string     `json:"notes"`
}

type batchRawMaterialDTO struct {
	ID       uuid.UUID  `json:"id"`
	ItemID   uuid.UUID  `json:"item_id"`
	UnitID   *uuid.UUID `json:"unit_id"`
	Quantity float64    `json:"quantity"`
	Cost     float64    `json:"cost"`
}

type qualityCheckDTO struct {
	ID          uuid.UUID  `json:"id"`
	InspectorID *uuid.UUID `json:"inspector_id"`
	Result      string     `json:"result"`
	Notes       string     `json:"notes"`
	CheckDate   time.Time  `json:"check_date"`
}

type productionBatchDTO struct {
	ID              uuid.UUID             `json:"id"`
	BatchNumber     string                `json:"batch_number"`
	RecipeID        uuid.UUID             `json:"recipe_id"`
	Status          string                `json:"status"`
	PlannedQuantity float64               `json:"planned_quantity"`
	ActualQuantity  *float64              `json:"actual_quantity"`
	LaborCost       float64               `json:"labor_cost"`
	OverheadCost    float64               `json:"overhead_cost"`
	ScrapQuantity   float64               `json:"scrap_quantity"`
	UnitCost        *float64              `json:"unit_cost"`
	OutletID        *uuid.UUID            `json:"outlet_id"`
	ScheduledDate   time.Time             `json:"scheduled_date"`
	StartDate       *time.Time            `json:"start_date"`
	EndDate         *time.Time            `json:"end_date"`
	Notes           string                `json:"notes"`
	RawMaterials    []batchRawMaterialDTO `json:"raw_materials,omitempty"`
	QualityChecks   []qualityCheckDTO     `json:"quality_checks,omitempty"`
	// Variance (populated on the detail view only).
	YieldVariance    *float64              `json:"yield_variance,omitempty"`     // actual - planned output
	YieldVariancePct *float64             `json:"yield_variance_pct,omitempty"` // (actual-planned)/planned * 100
	MaterialVariance []materialVarianceDTO `json:"material_variance,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}

// materialVarianceDTO compares the BOM-implied planned quantity for a line against
// what the batch actually consumed.
type materialVarianceDTO struct {
	ItemID   uuid.UUID `json:"item_id"`
	ItemSku  string    `json:"item_sku"`
	Planned  float64   `json:"planned"`
	Actual   float64   `json:"actual"`
	Variance float64   `json:"variance"`
}

func productionBatchToDTO(b *ent.ProductionBatch, mats []*ent.BatchRawMaterial, qcs []*ent.QualityCheck) productionBatchDTO {
	dto := productionBatchDTO{
		ID: b.ID, BatchNumber: b.BatchNumber, RecipeID: b.RecipeID, Status: string(b.Status),
		PlannedQuantity: b.PlannedQuantity, ActualQuantity: b.ActualQuantity,
		LaborCost: b.LaborCost, OverheadCost: b.OverheadCost, ScrapQuantity: b.ScrapQuantity, UnitCost: b.UnitCost, OutletID: b.OutletID,
		ScheduledDate: b.ScheduledDate, StartDate: b.StartDate, EndDate: b.EndDate,
		Notes: b.Notes, CreatedAt: b.CreatedAt,
	}
	for _, m := range mats {
		dto.RawMaterials = append(dto.RawMaterials, batchRawMaterialDTO{ID: m.ID, ItemID: m.ItemID, UnitID: m.UnitID, Quantity: m.Quantity, Cost: m.Cost})
	}
	for _, qc := range qcs {
		dto.QualityChecks = append(dto.QualityChecks, qualityCheckDTO{ID: qc.ID, InspectorID: qc.InspectorID, Result: string(qc.Result), Notes: qc.Notes, CheckDate: qc.CheckDate})
	}
	return dto
}

func (h *InventoryExtrasHandler) registerManufacturingRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/production-batches", h.ListProductionBatches)
	r.Get("/inventory/manufacturing/material-check", h.CheckMaterialAvailability)
	r.Get("/inventory/production-batches/{batchID}", h.GetProductionBatch)
	r.With(perm(add)).Post("/inventory/production-batches", h.CreateProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/start", h.StartProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/complete", h.CompleteProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/cancel", h.CancelProductionBatch)
	r.Get("/inventory/production-batches/{batchID}/materials", h.ListBatchMaterials)
	r.Get("/inventory/production-batches/{batchID}/usage", h.ListRawMaterialUsage)
	r.Get("/inventory/production-batches/{batchID}/quality-checks", h.ListQualityChecks)
	r.With(perm(add)).Post("/inventory/production-batches/{batchID}/quality-checks", h.CreateQualityCheck)
}

// rawMaterialUsageDTO is the audit-trail row for raw-material movements.
type rawMaterialUsageDTO struct {
	ID              uuid.UUID  `json:"id"`
	RawItemID       uuid.UUID  `json:"raw_item_id"`
	RawSKU          string     `json:"raw_sku"`
	FinishedItemID  *uuid.UUID `json:"finished_item_id,omitempty"`
	Quantity        float64    `json:"quantity"`
	Cost            float64    `json:"cost"`
	TransactionType string     `json:"transaction_type"`
	OccurredAt      time.Time  `json:"occurred_at"`
}

// ListRawMaterialUsage handles GET /inventory/production-batches/{batchID}/usage.
//
//	@Summary      Raw-material usage audit trail for a production batch
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Success      200      {array}   rawMaterialUsageDTO
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/usage [get]
func (h *InventoryExtrasHandler) ListRawMaterialUsage(w http.ResponseWriter, r *http.Request) {
	_, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.RawMaterialUsage.Query().
		Where(entrmu.ProductionBatchID(b.ID)).
		Order(ent.Desc(entrmu.FieldOccurredAt)).
		All(r.Context())
	out := make([]rawMaterialUsageDTO, len(rows))
	for i, u := range rows {
		out[i] = rawMaterialUsageDTO{
			ID: u.ID, RawItemID: u.RawItemID, RawSKU: u.RawSku, FinishedItemID: u.FinishedItemID,
			Quantity: u.Quantity, Cost: u.Cost, TransactionType: string(u.TransactionType), OccurredAt: u.OccurredAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ListProductionBatches handles GET /inventory/production-batches.
//
//	@Summary      List production batches
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        status     query     string  false  "Filter by status"
//	@Param        recipe_id  query     string  false  "Filter by recipe ID"
//	@Success      200        {object}  map[string]interface{}  "Paginated list of productionBatchDTO"
//	@Failure      400        {object}  map[string]string
//	@Failure      500        {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches [get]
func (h *InventoryExtrasHandler) ListProductionBatches(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.ProductionBatch.Query().Where(entpb.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entpb.StatusEQ(entpb.Status(s)))
	}
	if rc := r.URL.Query().Get("recipe_id"); rc != "" {
		if id, e := uuid.Parse(rc); e == nil {
			q = q.Where(entpb.RecipeID(id))
		}
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entpb.FieldCreatedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list production batches")
		return
	}
	out := make([]productionBatchDTO, len(rows))
	for i, b := range rows {
		out[i] = productionBatchToDTO(b, nil, nil)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// GetProductionBatch handles GET /inventory/production-batches/{batchID}.
//
//	@Summary      Get a production batch with materials and QC
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Success      200      {object}  productionBatchDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID} [get]
func (h *InventoryExtrasHandler) GetProductionBatch(w http.ResponseWriter, r *http.Request) {
	_, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	tenantID, _ := parseTenantID(r)
	mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
	qcs, _ := h.orm.QualityCheck.Query().Where(entqc.ProductionBatchID(b.ID)).All(r.Context())
	dto := productionBatchToDTO(b, mats, qcs)

	// Yield variance: actual output vs plan.
	if b.ActualQuantity != nil {
		yv := *b.ActualQuantity - b.PlannedQuantity
		dto.YieldVariance = &yv
		if b.PlannedQuantity > 0 {
			pct := yv / b.PlannedQuantity * 100
			dto.YieldVariancePct = &pct
		}
	}
	// Material variance: BOM-implied planned (recipe ratio) vs actually consumed.
	if recipe, e := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID), entrecipe.TenantID(tenantID)).Only(r.Context()); e == nil && len(mats) > 0 {
		ratio := 1.0
		if recipe.OutputQty > 0 {
			ratio = b.PlannedQuantity / recipe.OutputQty
		}
		ings, _ := h.orm.RecipeIngredient.Query().Where(entri.RecipeID(recipe.ID)).All(r.Context())
		plannedByItem := make(map[uuid.UUID]float64, len(ings))
		skuByItem := make(map[uuid.UUID]string, len(ings))
		for _, ing := range ings {
			plannedByItem[ing.ItemID] = ing.Quantity * ratio
			skuByItem[ing.ItemID] = ing.ItemSku
		}
		for _, m := range mats {
			planned := plannedByItem[m.ItemID]
			dto.MaterialVariance = append(dto.MaterialVariance, materialVarianceDTO{
				ItemID: m.ItemID, ItemSku: skuByItem[m.ItemID], Planned: planned, Actual: m.Quantity, Variance: m.Quantity - planned,
			})
		}
	}
	writeJSON(w, http.StatusOK, dto)
}

// CreateProductionBatch handles POST /inventory/production-batches.
//
//	@Summary      Create a production batch
//	@Tags         Manufacturing
//	@Accept       json
//	@Produce      json
//	@Param        body  body      productionBatchPayload  true  "Production batch payload"
//	@Success      201   {object}  productionBatchDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches [post]
func (h *InventoryExtrasHandler) CreateProductionBatch(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req productionBatchPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.RecipeID == uuid.Nil || req.PlannedQuantity <= 0 {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "recipe_id and a positive planned_quantity are required")
		return
	}
	batchNo := "BATCH-" + strings.ToUpper(uuid.New().String()[:8])
	create := h.orm.ProductionBatch.Create().
		SetTenantID(tenantID).SetBatchNumber(batchNo).SetRecipeID(req.RecipeID).
		SetPlannedQuantity(req.PlannedQuantity).SetLaborCost(req.LaborCost).
		SetOverheadCost(req.OverheadCost).SetNotes(req.Notes)
	if req.ScheduledDate != nil {
		create = create.SetScheduledDate(*req.ScheduledDate)
	} else {
		create = create.SetScheduledDate(time.Now().UTC())
	}
	if req.OutletID != nil {
		create = create.SetOutletID(*req.OutletID)
	}
	if req.SupervisorID != nil {
		create = create.SetSupervisorID(*req.SupervisorID)
	}
	if req.CreatedBy != nil {
		create = create.SetCreatedBy(*req.CreatedBy)
	}
	b, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create production batch failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create production batch")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "production_batch", b.ID, "inventory.production.created", map[string]any{
		"id": b.ID, "batch_number": b.BatchNumber, "recipe_id": b.RecipeID, "planned_quantity": b.PlannedQuantity,
	})
	writeJSON(w, http.StatusCreated, productionBatchToDTO(b, nil, nil))
}

// StartProductionBatch explodes the recipe BOM into BatchRawMaterial rows and
// emits a consumption event for the stock module.
//
//	@Summary      Start a production batch (consume raw materials)
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Success      200      {object}  productionBatchDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/start [post]
func (h *InventoryExtrasHandler) StartProductionBatch(w http.ResponseWriter, r *http.Request) {
	tenantID, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	if b.Status != entpb.StatusPlanned {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "Only planned batches can be started")
		return
	}
	// Approval gate: a production run commits raw materials, so gate the start of a
	// batch when a production_batch approval rule is configured.
	if !h.gateApproval(w, r, tenantID, "production_batch", b.ID, b.BatchNumber, 0) {
		return
	}
	recipe, err := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID), entrecipe.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "RECIPE_NOT_FOUND", "Batch recipe not found")
		return
	}
	// Material-availability pre-check (skip with ?force=true). A detergent batch
	// must not start if a chemical raw material is short.
	if r.URL.Query().Get("force") != "true" {
		if shortages, e := h.computeMaterialShortages(r.Context(), tenantID, b.RecipeID, b.PlannedQuantity); e == nil && len(shortages) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "Insufficient raw materials to start this batch", "code": "INSUFFICIENT_MATERIALS", "shortages": shortages,
			})
			return
		}
	}
	ratio := 1.0
	if recipe.OutputQty > 0 {
		ratio = b.PlannedQuantity / recipe.OutputQty
	}
	ings, _ := h.orm.RecipeIngredient.Query().Where(entri.RecipeID(recipe.ID)).All(r.Context())
	consumed := make([]map[string]any, 0, len(ings))
	consumeItems := make([]stock.ConsumptionItem, 0, len(ings))
	for _, ing := range ings {
		qty := ing.Quantity * ratio
		// Material cost = item.cost_price * qty (rolled into unit cost at completion).
		matCost := 0.0
		if it, e := h.orm.Item.Get(r.Context(), ing.ItemID); e == nil && it.CostPrice != nil {
			matCost = *it.CostPrice * qty
		}
		mc := h.orm.BatchRawMaterial.Create().
			SetTenantID(tenantID).SetProductionBatchID(b.ID).SetItemID(ing.ItemID).SetQuantity(qty).SetCost(matCost)
		if ing.UnitID != nil {
			mc = mc.SetUnitID(*ing.UnitID)
		}
		if _, err := mc.Save(r.Context()); err != nil {
			h.log.Warn("create batch raw material failed", zap.Error(err))
			continue
		}
		consumed = append(consumed, map[string]any{"item_id": ing.ItemID, "quantity": qty, "unit_id": ing.UnitID})
		if ing.ItemSku != "" {
			consumeItems = append(consumeItems, stock.ConsumptionItem{SKU: ing.ItemSku, Quantity: qty})
		}
		// Audit trail: raw material issued to production.
		h.recordRawMaterialUsage(r.Context(), tenantID, b.ID, recipe.ItemID, ing.ItemID, ing.ItemSku, qty, matCost, "production")
	}
	updated, err := h.orm.ProductionBatch.UpdateOneID(b.ID).SetStatus(entpb.StatusInProgress).SetStartDate(time.Now().UTC()).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to start batch")
		return
	}
	// Apply real raw-material consumption (stock-out) in-process.
	if h.stockSvc != nil && len(consumeItems) > 0 {
		if _, err := h.stockSvc.RecordConsumption(r.Context(), tenantID, stock.ConsumptionRequest{
			TenantID:       tenantID,
			OrderID:        b.ID,
			Items:          consumeItems,
			Reason:         "production",
			IdempotencyKey: "production-start-" + b.ID.String(),
		}); err != nil {
			h.log.Warn("production start: stock consumption failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "production_batch", updated.ID, "inventory.production.started", map[string]any{
		"id": updated.ID, "batch_number": updated.BatchNumber, "consumed_materials": consumed,
	})
	mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(updated.ID)).All(r.Context())
	writeJSON(w, http.StatusOK, productionBatchToDTO(updated, mats, nil))
}

// CompleteProductionBatch finalizes the batch, computes cost, and emits a
// finished-goods receipt event for the stock module.
//
//	@Summary      Complete a production batch (receive finished goods)
//	@Tags         Manufacturing
//	@Accept       json
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Param        body     body      object  true  "actual_quantity"
//	@Success      200      {object}  productionBatchDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/complete [post]
func (h *InventoryExtrasHandler) CompleteProductionBatch(w http.ResponseWriter, r *http.Request) {
	tenantID, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	if b.Status != entpb.StatusInProgress {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "Only in-progress batches can be completed")
		return
	}
	var body struct {
		ActualQuantity float64 `json:"actual_quantity"`
		ScrapQuantity  float64 `json:"scrap_quantity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ActualQuantity <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "A positive actual_quantity is required")
		return
	}
	recipe, _ := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID)).Only(r.Context())
	// QC gate: recipes flagged requires_qc need a passing quality check first.
	if recipe != nil && recipe.RequiresQc {
		passed, _ := h.orm.QualityCheck.Query().Where(entqc.ProductionBatchID(b.ID), entqc.ResultEQ(entqc.ResultPass)).Count(r.Context())
		if passed == 0 {
			writeError(w, http.StatusBadRequest, "QC_REQUIRED", "This recipe requires a passing quality check before completion")
			return
		}
	}
	// Batch cost = raw materials (set at start) + labor + overhead → unit cost over actual output.
	mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
	materialCost := 0.0
	for _, m := range mats {
		materialCost += m.Cost
	}
	totalCost := materialCost + b.LaborCost + b.OverheadCost
	unitCost := 0.0
	if body.ActualQuantity > 0 {
		unitCost = totalCost / body.ActualQuantity
	}
	updated, err := h.orm.ProductionBatch.UpdateOneID(b.ID).
		SetStatus(entpb.StatusCompleted).SetActualQuantity(body.ActualQuantity).
		SetScrapQuantity(body.ScrapQuantity).SetUnitCost(unitCost).SetEndDate(time.Now().UTC()).
		Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete batch")
		return
	}
	payload := map[string]any{
		"id": updated.ID, "batch_number": updated.BatchNumber, "actual_quantity": body.ActualQuantity, "scrap_quantity": body.ScrapQuantity,
		"material_cost": materialCost, "labor_cost": b.LaborCost, "overhead_cost": b.OverheadCost, "total_cost": totalCost, "unit_cost": unitCost,
	}
	if recipe != nil && recipe.ItemID != nil {
		payload["finished_item_id"] = *recipe.ItemID
	}
	// Receive finished goods into stock (stock-in) in-process.
	if h.stockSvc != nil && recipe != nil {
		fgSKU := recipe.Sku
		if recipe.ItemID != nil {
			if s := h.skuForItem(r.Context(), tenantID, *recipe.ItemID); s != "" {
				fgSKU = s
			}
		}
		if fgSKU != "" {
			if err := h.stockSvc.RestockItems(r.Context(), tenantID, uuid.UUID{},
				[]stock.RestockItem{{SKU: fgSKU, Quantity: body.ActualQuantity}},
				"production-complete-"+updated.ID.String()); err != nil {
				h.log.Warn("production complete: finished-goods restock failed", zap.Error(err))
			}
		}
	}
	h.publishOutbox(r.Context(), tenantID, "production_batch", updated.ID, "inventory.production.completed", payload)
	// Keep the persisted daily analytics snapshots fresh (best-effort).
	_, _ = h.recomputeManufacturingAnalytics(r.Context(), tenantID)
	writeJSON(w, http.StatusOK, productionBatchToDTO(updated, nil, nil))
}

// CancelProductionBatch handles POST /inventory/production-batches/{batchID}/cancel.
//
//	@Summary      Cancel a production batch
//	@Tags         Manufacturing
//	@Accept       json
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Param        body     body      object  false  "reason"
//	@Success      200      {object}  productionBatchDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/cancel [post]
func (h *InventoryExtrasHandler) CancelProductionBatch(w http.ResponseWriter, r *http.Request) {
	tenantID, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	notes := b.Notes
	if body.Reason != "" {
		notes = strings.TrimSpace(notes + "\nCancelled: " + body.Reason)
	}

	// Rollback: if the batch was already started, return the consumed raw materials
	// to stock and record the reversal in the usage audit trail.
	returned := make([]map[string]any, 0)
	if b.Status == entpb.StatusInProgress {
		var finishedItemID *uuid.UUID
		if recipe, e := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID)).Only(r.Context()); e == nil {
			finishedItemID = recipe.ItemID
		}
		mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
		restock := make([]stock.RestockItem, 0, len(mats))
		for _, m := range mats {
			sku := h.skuForItem(r.Context(), tenantID, m.ItemID)
			if sku != "" {
				restock = append(restock, stock.RestockItem{SKU: sku, Quantity: m.Quantity})
			}
			h.recordRawMaterialUsage(r.Context(), tenantID, b.ID, finishedItemID, m.ItemID, sku, m.Quantity, m.Cost, "return")
			returned = append(returned, map[string]any{"item_id": m.ItemID, "quantity": m.Quantity})
		}
		if h.stockSvc != nil && len(restock) > 0 {
			if err := h.stockSvc.RestockItems(r.Context(), tenantID, uuid.UUID{}, restock, "production-cancel-"+b.ID.String()); err != nil {
				h.log.Warn("production cancel: material restock failed", zap.Error(err))
			}
		}
	}

	updated, err := h.orm.ProductionBatch.UpdateOneID(b.ID).SetStatus(entpb.StatusCancelled).SetNotes(notes).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to cancel batch")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "production_batch", updated.ID, "inventory.production.cancelled", map[string]any{"id": updated.ID, "reason": body.Reason, "returned_materials": returned})
	writeJSON(w, http.StatusOK, productionBatchToDTO(updated, nil, nil))
}

// ListBatchMaterials handles GET /inventory/production-batches/{batchID}/materials.
//
//	@Summary      List raw materials consumed by a production batch
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Success      200      {array}   batchRawMaterialDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/materials [get]
func (h *InventoryExtrasHandler) ListBatchMaterials(w http.ResponseWriter, r *http.Request) {
	_, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
	out := make([]batchRawMaterialDTO, len(mats))
	for i, m := range mats {
		out[i] = batchRawMaterialDTO{ID: m.ID, ItemID: m.ItemID, UnitID: m.UnitID, Quantity: m.Quantity, Cost: m.Cost}
	}
	writeJSON(w, http.StatusOK, out)
}

// ListQualityChecks handles GET /inventory/production-batches/{batchID}/quality-checks.
//
//	@Summary      List quality checks for a production batch
//	@Tags         Manufacturing
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Success      200      {array}   qualityCheckDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/quality-checks [get]
func (h *InventoryExtrasHandler) ListQualityChecks(w http.ResponseWriter, r *http.Request) {
	_, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	qcs, _ := h.orm.QualityCheck.Query().Where(entqc.ProductionBatchID(b.ID)).All(r.Context())
	out := make([]qualityCheckDTO, len(qcs))
	for i, qc := range qcs {
		out[i] = qualityCheckDTO{ID: qc.ID, InspectorID: qc.InspectorID, Result: string(qc.Result), Notes: qc.Notes, CheckDate: qc.CheckDate}
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateQualityCheck handles POST /inventory/production-batches/{batchID}/quality-checks.
//
//	@Summary      Record a quality check for a production batch
//	@Tags         Manufacturing
//	@Accept       json
//	@Produce      json
//	@Param        batchID  path      string  true  "Production batch ID"
//	@Param        body     body      object  true  "Quality check result, notes and inspector"
//	@Success      201      {object}  qualityCheckDTO
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/production-batches/{batchID}/quality-checks [post]
func (h *InventoryExtrasHandler) CreateQualityCheck(w http.ResponseWriter, r *http.Request) {
	tenantID, b, ok := h.loadBatch(w, r)
	if !ok {
		return
	}
	var body struct {
		Result      string     `json:"result"`
		Notes       string     `json:"notes"`
		InspectorID *uuid.UUID `json:"inspector_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Result == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "result is required")
		return
	}
	create := h.orm.QualityCheck.Create().
		SetTenantID(tenantID).SetProductionBatchID(b.ID).SetResult(entqc.Result(body.Result)).SetNotes(body.Notes)
	if body.InspectorID != nil {
		create = create.SetInspectorID(*body.InspectorID)
	}
	qc, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create quality check failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create quality check")
		return
	}
	// On fail, fail the batch and book the consumed materials as wastage.
	if qc.Result == entqc.ResultFail {
		_, _ = h.orm.ProductionBatch.UpdateOneID(b.ID).SetStatus(entpb.StatusFailed).Save(r.Context())
		var finishedItemID *uuid.UUID
		if recipe, e := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID)).Only(r.Context()); e == nil {
			finishedItemID = recipe.ItemID
		}
		mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
		for _, m := range mats {
			h.recordRawMaterialUsage(r.Context(), tenantID, b.ID, finishedItemID, m.ItemID, h.skuForItem(r.Context(), tenantID, m.ItemID), m.Quantity, m.Cost, "wastage")
		}
		h.publishOutbox(r.Context(), tenantID, "production_batch", b.ID, "inventory.production.qc_failed", map[string]any{"id": b.ID, "quality_check_id": qc.ID})
	}
	writeJSON(w, http.StatusCreated, qualityCheckDTO{ID: qc.ID, InspectorID: qc.InspectorID, Result: string(qc.Result), Notes: qc.Notes, CheckDate: qc.CheckDate})
}

func (h *InventoryExtrasHandler) loadBatch(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.ProductionBatch, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "batchID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid batch ID")
		return uuid.Nil, nil, false
	}
	b, err := h.orm.ProductionBatch.Query().Where(entpb.ID(id), entpb.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Production batch not found")
		return uuid.Nil, nil, false
	}
	return tenantID, b, true
}
