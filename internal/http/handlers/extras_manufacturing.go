package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entbrm "github.com/bengobox/inventory-service/internal/ent/batchrawmaterial"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
	entqc "github.com/bengobox/inventory-service/internal/ent/qualitycheck"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
	entri "github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

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
	OutletID        *uuid.UUID            `json:"outlet_id"`
	ScheduledDate   time.Time             `json:"scheduled_date"`
	StartDate       *time.Time            `json:"start_date"`
	EndDate         *time.Time            `json:"end_date"`
	Notes           string                `json:"notes"`
	RawMaterials    []batchRawMaterialDTO `json:"raw_materials,omitempty"`
	QualityChecks   []qualityCheckDTO     `json:"quality_checks,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}

func productionBatchToDTO(b *ent.ProductionBatch, mats []*ent.BatchRawMaterial, qcs []*ent.QualityCheck) productionBatchDTO {
	dto := productionBatchDTO{
		ID: b.ID, BatchNumber: b.BatchNumber, RecipeID: b.RecipeID, Status: string(b.Status),
		PlannedQuantity: b.PlannedQuantity, ActualQuantity: b.ActualQuantity,
		LaborCost: b.LaborCost, OverheadCost: b.OverheadCost, OutletID: b.OutletID,
		ScheduledDate: b.ScheduledDate, StartDate: b.StartDate, EndDate: b.EndDate,
		Notes: b.Notes, CreatedAt: b.CreatedAt,
	}
	for _, m := range mats {
		dto.RawMaterials = append(dto.RawMaterials, batchRawMaterialDTO{ID: m.ID, ItemID: m.ItemID, UnitID: m.UnitID, Quantity: m.Quantity})
	}
	for _, qc := range qcs {
		dto.QualityChecks = append(dto.QualityChecks, qualityCheckDTO{ID: qc.ID, InspectorID: qc.InspectorID, Result: string(qc.Result), Notes: qc.Notes, CheckDate: qc.CheckDate})
	}
	return dto
}

func (h *InventoryExtrasHandler) registerManufacturingRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/production-batches", h.ListProductionBatches)
	r.Get("/inventory/production-batches/{batchID}", h.GetProductionBatch)
	r.With(perm(add)).Post("/inventory/production-batches", h.CreateProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/start", h.StartProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/complete", h.CompleteProductionBatch)
	r.With(perm(change)).Post("/inventory/production-batches/{batchID}/cancel", h.CancelProductionBatch)
	r.Get("/inventory/production-batches/{batchID}/materials", h.ListBatchMaterials)
	r.Get("/inventory/production-batches/{batchID}/quality-checks", h.ListQualityChecks)
	r.With(perm(add)).Post("/inventory/production-batches/{batchID}/quality-checks", h.CreateQualityCheck)
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
	mats, _ := h.orm.BatchRawMaterial.Query().Where(entbrm.ProductionBatchID(b.ID)).All(r.Context())
	qcs, _ := h.orm.QualityCheck.Query().Where(entqc.ProductionBatchID(b.ID)).All(r.Context())
	writeJSON(w, http.StatusOK, productionBatchToDTO(b, mats, qcs))
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
	recipe, err := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID), entrecipe.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "RECIPE_NOT_FOUND", "Batch recipe not found")
		return
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
		mc := h.orm.BatchRawMaterial.Create().
			SetTenantID(tenantID).SetProductionBatchID(b.ID).SetItemID(ing.ItemID).SetQuantity(qty)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ActualQuantity <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "A positive actual_quantity is required")
		return
	}
	totalCost := b.LaborCost + b.OverheadCost
	unitCost := 0.0
	if body.ActualQuantity > 0 {
		unitCost = totalCost / body.ActualQuantity
	}
	updated, err := h.orm.ProductionBatch.UpdateOneID(b.ID).
		SetStatus(entpb.StatusCompleted).SetActualQuantity(body.ActualQuantity).SetEndDate(time.Now().UTC()).
		Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete batch")
		return
	}
	recipe, _ := h.orm.Recipe.Query().Where(entrecipe.ID(b.RecipeID)).Only(r.Context())
	payload := map[string]any{
		"id": updated.ID, "batch_number": updated.BatchNumber, "actual_quantity": body.ActualQuantity,
		"labor_cost": b.LaborCost, "overhead_cost": b.OverheadCost, "total_cost": totalCost, "unit_cost": unitCost,
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
	updated, err := h.orm.ProductionBatch.UpdateOneID(b.ID).SetStatus(entpb.StatusCancelled).SetNotes(notes).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to cancel batch")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "production_batch", updated.ID, "inventory.production.cancelled", map[string]any{"id": updated.ID, "reason": body.Reason})
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
		out[i] = batchRawMaterialDTO{ID: m.ID, ItemID: m.ItemID, UnitID: m.UnitID, Quantity: m.Quantity}
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
	// On fail, fail the batch.
	if qc.Result == entqc.ResultFail {
		_, _ = h.orm.ProductionBatch.UpdateOneID(b.ID).SetStatus(entpb.StatusFailed).Save(r.Context())
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
