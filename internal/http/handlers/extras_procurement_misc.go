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
	entsd "github.com/bengobox/inventory-service/internal/ent/servicedelivery"
	entsp "github.com/bengobox/inventory-service/internal/ent/supplierperformance"
)

// ─── Supplier Performance + Service Delivery (procurement) ──────────────────
// Migrated from ERP procurement.supplier_performance + procurement.requisitions.ServiceDelivery.

func (h *InventoryExtrasHandler) registerProcurementMiscRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/supplier-performance", h.ListSupplierPerformance)
	r.With(perm(add)).Post("/inventory/supplier-performance", h.RecordSupplierPerformance)
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
