package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entsd "github.com/bengobox/inventory-service/internal/ent/servicedelivery"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entsp "github.com/bengobox/inventory-service/internal/ent/supplierperformance"
)

// ─── Supplier Performance + Service Delivery (procurement) ──────────────────
// Migrated from ERP procurement.supplier_performance + procurement.requisitions.ServiceDelivery.

func (h *InventoryExtrasHandler) registerProcurementMiscRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/supplier-performance", h.ListSupplierPerformance)
	r.With(perm(add)).Post("/inventory/supplier-performance", h.RecordSupplierPerformance)
	r.With(perm(change)).Post("/inventory/supplier-performance/recompute", h.RecomputeSupplierPerformance)
	r.Get("/inventory/service-deliveries", h.ListServiceDeliveries)
	r.With(perm(add)).Post("/inventory/service-deliveries", h.CreateServiceDelivery)
	r.With(perm(change)).Put("/inventory/service-deliveries/{sdID}/status", h.UpdateServiceDeliveryStatus)
}

// --- Supplier performance ---

type supplierPerfPayload struct {
	SupplierID          uuid.UUID  `json:"supplier_id"`
	PeriodStart         *time.Time `json:"period_start"`
	PeriodEnd           *time.Time `json:"period_end"`
	OnTimeDeliveryRate  float64    `json:"on_time_delivery_rate"`
	DefectRate          float64    `json:"defect_rate"`
	AverageLeadTimeDays float64    `json:"average_lead_time_days"`
	TotalSpend          float64    `json:"total_spend"`
}

type supplierPerfDTO struct {
	ID                  uuid.UUID `json:"id"`
	SupplierID          uuid.UUID `json:"supplier_id"`
	PeriodStart         time.Time `json:"period_start"`
	PeriodEnd           time.Time `json:"period_end"`
	OnTimeDeliveryRate  float64   `json:"on_time_delivery_rate"`
	DefectRate          float64   `json:"defect_rate"`
	AverageLeadTimeDays float64   `json:"average_lead_time_days"`
	TotalSpend          float64   `json:"total_spend"`
}

func supplierPerfToDTO(s *ent.SupplierPerformance) supplierPerfDTO {
	return supplierPerfDTO{
		ID: s.ID, SupplierID: s.SupplierID, PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd,
		OnTimeDeliveryRate: s.OnTimeDeliveryRate, DefectRate: s.DefectRate,
		AverageLeadTimeDays: s.AverageLeadTimeDays, TotalSpend: s.TotalSpend,
	}
}

// ListSupplierPerformance handles GET /inventory/supplier-performance.
//
//	@Summary      List supplier performance records
//	@Tags         Procurement
//	@Produce      json
//	@Param        supplier_id  query     string  false  "Filter by supplier ID"
//	@Success      200          {object}  map[string]interface{}  "Paginated list of supplierPerfDTO"
//	@Failure      400          {object}  map[string]string
//	@Failure      500          {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/supplier-performance [get]
func (h *InventoryExtrasHandler) ListSupplierPerformance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.SupplierPerformance.Query().Where(entsp.TenantID(tenantID))
	if sup := r.URL.Query().Get("supplier_id"); sup != "" {
		if id, e := uuid.Parse(sup); e == nil {
			q = q.Where(entsp.SupplierID(id))
		}
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entsp.FieldPeriodEnd)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list supplier performance")
		return
	}
	out := make([]supplierPerfDTO, len(rows))
	for i, s := range rows {
		out[i] = supplierPerfToDTO(s)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// RecordSupplierPerformance handles POST /inventory/supplier-performance.
//
//	@Summary      Record a supplier performance evaluation
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      supplierPerfPayload  true  "Supplier performance payload"
//	@Success      201   {object}  supplierPerfDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/supplier-performance [post]
func (h *InventoryExtrasHandler) RecordSupplierPerformance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req supplierPerfPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.SupplierID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "supplier_id is required")
		return
	}
	create := h.orm.SupplierPerformance.Create().
		SetTenantID(tenantID).SetSupplierID(req.SupplierID).
		SetOnTimeDeliveryRate(req.OnTimeDeliveryRate).SetDefectRate(req.DefectRate).
		SetAverageLeadTimeDays(req.AverageLeadTimeDays).SetTotalSpend(req.TotalSpend)
	if req.PeriodStart != nil {
		create = create.SetPeriodStart(*req.PeriodStart)
	} else {
		create = create.SetPeriodStart(time.Now().UTC().AddDate(0, -1, 0))
	}
	if req.PeriodEnd != nil {
		create = create.SetPeriodEnd(*req.PeriodEnd)
	} else {
		create = create.SetPeriodEnd(time.Now().UTC())
	}
	s, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("record supplier performance failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to record supplier performance")
		return
	}
	writeJSON(w, http.StatusCreated, supplierPerfToDTO(s))
}

// RecomputeSupplierPerformance handles POST /inventory/supplier-performance/recompute.
// Computes a fresh performance snapshot per active supplier from PO + GRN data
// (total spend, on-time %, defect rate, average lead time) over the trailing
// quarter. Enabled by the GRN data captured at goods receipt.
//
//	@Summary  Recompute supplier performance from PO + GRN data
//	@Tags     Procurement
//	@Produce  json
//	@Success  200  {object}  map[string]interface{}
//	@Security bearerAuth
//	@Router   /{tenant}/inventory/supplier-performance/recompute [post]
func (h *InventoryExtrasHandler) RecomputeSupplierPerformance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	periodStart := now.AddDate(0, -3, 0) // trailing quarter

	suppliers, _ := h.orm.Supplier.Query().Where(entsupplier.TenantID(tenantID), entsupplier.IsActive(true)).All(ctx)
	computed := 0
	for _, s := range suppliers {
		pos, _ := h.orm.PurchaseOrder.Query().Where(entpo.TenantID(tenantID), entpo.SupplierID(s.ID)).All(ctx)
		if len(pos) == 0 {
			continue
		}
		poByID := make(map[uuid.UUID]*ent.PurchaseOrder, len(pos))
		poIDs := make([]uuid.UUID, 0, len(pos))
		var totalSpend float64
		for _, po := range pos {
			poByID[po.ID] = po
			poIDs = append(poIDs, po.ID)
			if po.Status == entpo.StatusReceived || po.Status == entpo.StatusPartiallyReceived {
				totalSpend += po.TotalAmount
			}
		}

		grns, _ := h.orm.GoodsReceipt.Query().
			Where(entgr.TenantID(tenantID), entgr.PurchaseOrderIDIn(poIDs...), entgr.StatusEQ(entgr.StatusPosted)).
			All(ctx)
		var onTime, totalGRN, leadDaysSum float64
		grnIDs := make([]uuid.UUID, 0, len(grns))
		for _, g := range grns {
			grnIDs = append(grnIDs, g.ID)
			totalGRN++
			if po := poByID[g.PurchaseOrderID]; po != nil {
				leadDaysSum += g.ReceivedDate.Sub(po.CreatedAt).Hours() / 24
				if po.ExpectedDate != nil && !g.ReceivedDate.After(*po.ExpectedDate) {
					onTime++
				}
			}
		}

		var recv, rej float64
		if len(grnIDs) > 0 {
			lines, _ := h.orm.GoodsReceiptLine.Query().
				Where(entgrl.TenantID(tenantID), entgrl.GoodsReceiptIDIn(grnIDs...)).All(ctx)
			for _, l := range lines {
				recv += float64(l.QuantityReceived)
				rej += float64(l.QuantityRejected)
			}
		}

		onTimeRate, defectRate, avgLead := 0.0, 0.0, 0.0
		if totalGRN > 0 {
			onTimeRate = onTime / totalGRN
			avgLead = leadDaysSum / totalGRN
		}
		if recv > 0 {
			defectRate = rej / recv
		}
		if _, e := h.orm.SupplierPerformance.Create().
			SetTenantID(tenantID).SetSupplierID(s.ID).
			SetPeriodStart(periodStart).SetPeriodEnd(now).
			SetOnTimeDeliveryRate(onTimeRate).SetDefectRate(defectRate).
			SetAverageLeadTimeDays(avgLead).SetTotalSpend(totalSpend).
			Save(ctx); e != nil {
			h.log.Warn("recompute supplier performance failed", zap.String("supplier", s.ID.String()), zap.Error(e))
		} else {
			computed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"computed": computed, "period_start": periodStart, "period_end": now})
}

// --- Service delivery ---

type serviceDeliveryPayload struct {
	RequisitionLineID *uuid.UUID `json:"requisition_line_id"`
	ProviderID        *uuid.UUID `json:"provider_id"`
	StartDate         *time.Time `json:"start_date"`
	EndDate           *time.Time `json:"end_date"`
	Deliverables      string     `json:"deliverables"`
}

type serviceDeliveryDTO struct {
	ID                uuid.UUID  `json:"id"`
	RequisitionLineID *uuid.UUID `json:"requisition_line_id"`
	ProviderID        *uuid.UUID `json:"provider_id"`
	StartDate         time.Time  `json:"start_date"`
	EndDate           time.Time  `json:"end_date"`
	Deliverables      string     `json:"deliverables"`
	Status            string     `json:"status"`
}

func serviceDeliveryToDTO(s *ent.ServiceDelivery) serviceDeliveryDTO {
	return serviceDeliveryDTO{
		ID: s.ID, RequisitionLineID: s.RequisitionLineID, ProviderID: s.ProviderID,
		StartDate: s.StartDate, EndDate: s.EndDate, Deliverables: s.Deliverables, Status: string(s.Status),
	}
}

// ListServiceDeliveries handles GET /inventory/service-deliveries.
//
//	@Summary      List service deliveries
//	@Tags         Procurement
//	@Produce      json
//	@Param        status  query     string  false  "Filter by status"
//	@Success      200     {object}  map[string]interface{}  "Paginated list of serviceDeliveryDTO"
//	@Failure      400     {object}  map[string]string
//	@Failure      500     {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/service-deliveries [get]
func (h *InventoryExtrasHandler) ListServiceDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.ServiceDelivery.Query().Where(entsd.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entsd.StatusEQ(entsd.Status(s)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entsd.FieldCreatedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list service deliveries")
		return
	}
	out := make([]serviceDeliveryDTO, len(rows))
	for i, s := range rows {
		out[i] = serviceDeliveryToDTO(s)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// CreateServiceDelivery handles POST /inventory/service-deliveries.
//
//	@Summary      Create a service delivery record
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      serviceDeliveryPayload  true  "Service delivery payload"
//	@Success      201   {object}  serviceDeliveryDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/service-deliveries [post]
func (h *InventoryExtrasHandler) CreateServiceDelivery(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req serviceDeliveryPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	create := h.orm.ServiceDelivery.Create().SetTenantID(tenantID).SetDeliverables(req.Deliverables)
	if req.RequisitionLineID != nil {
		create = create.SetRequisitionLineID(*req.RequisitionLineID)
	}
	if req.ProviderID != nil {
		create = create.SetProviderID(*req.ProviderID)
	}
	if req.StartDate != nil {
		create = create.SetStartDate(*req.StartDate)
	} else {
		create = create.SetStartDate(time.Now().UTC())
	}
	if req.EndDate != nil {
		create = create.SetEndDate(*req.EndDate)
	} else {
		create = create.SetEndDate(time.Now().UTC())
	}
	s, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create service delivery failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create service delivery")
		return
	}
	writeJSON(w, http.StatusCreated, serviceDeliveryToDTO(s))
}

// UpdateServiceDeliveryStatus handles PUT /inventory/service-deliveries/{sdID}/status.
//
//	@Summary      Update a service delivery status
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        sdID  path      string  true  "Service delivery ID"
//	@Param        body  body      object  true  "status"
//	@Success      200   {object}  serviceDeliveryDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/service-deliveries/{sdID}/status [put]
func (h *InventoryExtrasHandler) UpdateServiceDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sdID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid service delivery ID")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "status is required")
		return
	}
	existing, err := h.orm.ServiceDelivery.Query().Where(entsd.ID(id), entsd.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Service delivery not found")
		return
	}
	updated, err := h.orm.ServiceDelivery.UpdateOneID(existing.ID).SetStatus(entsd.Status(body.Status)).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update service delivery")
		return
	}
	writeJSON(w, http.StatusOK, serviceDeliveryToDTO(updated))
}
