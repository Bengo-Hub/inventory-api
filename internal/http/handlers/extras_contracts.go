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
	entcontract "github.com/bengobox/inventory-service/internal/ent/contract"
)

// ─── Supplier Contracts (procurement) ───────────────────────────────────────
// Migrated from ERP procurement.contracts.

type contractPayload struct {
	SupplierID uuid.UUID  `json:"supplier_id"`
	Title      string     `json:"title"`
	StartDate  *time.Time `json:"start_date"`
	EndDate    *time.Time `json:"end_date"`
	Value      float64    `json:"value"`
	Terms      string     `json:"terms"`
}

type contractDTO struct {
	ID         uuid.UUID `json:"id"`
	SupplierID uuid.UUID `json:"supplier_id"`
	Title      string    `json:"title"`
	StartDate  time.Time `json:"start_date"`
	EndDate    time.Time `json:"end_date"`
	Value      float64   `json:"value"`
	Status     string    `json:"status"`
	Terms      string    `json:"terms"`
	CreatedAt  time.Time `json:"created_at"`
}

func contractToDTO(c *ent.Contract) contractDTO {
	return contractDTO{
		ID: c.ID, SupplierID: c.SupplierID, Title: c.Title,
		StartDate: c.StartDate, EndDate: c.EndDate, Value: c.Value,
		Status: string(c.Status), Terms: c.Terms, CreatedAt: c.CreatedAt,
	}
}

func (h *InventoryExtrasHandler) registerContractRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/contracts", h.ListContracts)
	r.Get("/inventory/contracts/{contractID}", h.GetContract)
	r.With(perm(add)).Post("/inventory/contracts", h.CreateContract)
	r.With(perm(change)).Put("/inventory/contracts/{contractID}", h.UpdateContract)
	r.With(perm(change)).Post("/inventory/contracts/{contractID}/activate", h.ActivateContract)
	r.With(perm(change)).Post("/inventory/contracts/{contractID}/terminate", h.TerminateContract)
	r.With(perm(change)).Post("/inventory/contracts/{contractID}/link-order", h.LinkContractOrder)
}

func (h *InventoryExtrasHandler) ListContracts(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.Contract.Query().Where(entcontract.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entcontract.StatusEQ(entcontract.Status(s)))
	}
	if sup := r.URL.Query().Get("supplier_id"); sup != "" {
		if id, e := uuid.Parse(sup); e == nil {
			q = q.Where(entcontract.SupplierID(id))
		}
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entcontract.FieldCreatedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list contracts")
		return
	}
	out := make([]contractDTO, len(rows))
	for i, c := range rows {
		out[i] = contractToDTO(c)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

func (h *InventoryExtrasHandler) CreateContract(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req contractPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.SupplierID == uuid.Nil || req.Title == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "supplier_id and title are required")
		return
	}
	create := h.orm.Contract.Create().
		SetTenantID(tenantID).SetSupplierID(req.SupplierID).SetTitle(req.Title).
		SetValue(req.Value).SetTerms(req.Terms)
	if req.StartDate != nil {
		create = create.SetStartDate(*req.StartDate)
	} else {
		create = create.SetStartDate(time.Now().UTC())
	}
	if req.EndDate != nil {
		create = create.SetEndDate(*req.EndDate)
	} else {
		create = create.SetEndDate(time.Now().UTC().AddDate(1, 0, 0))
	}
	c, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create contract")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "contract", c.ID, "inventory.contract.created", map[string]any{
		"id": c.ID, "supplier_id": c.SupplierID, "title": c.Title, "value": c.Value,
	})
	writeJSON(w, http.StatusCreated, contractToDTO(c))
}

func (h *InventoryExtrasHandler) GetContract(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.loadContract(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, contractToDTO(c))
}

func (h *InventoryExtrasHandler) UpdateContract(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.loadContract(w, r)
	if !ok {
		return
	}
	var req contractPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	upd := h.orm.Contract.UpdateOneID(c.ID).SetValue(req.Value).SetTerms(req.Terms)
	if req.Title != "" {
		upd = upd.SetTitle(req.Title)
	}
	if req.StartDate != nil {
		upd = upd.SetStartDate(*req.StartDate)
	}
	if req.EndDate != nil {
		upd = upd.SetEndDate(*req.EndDate)
	}
	updated, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update contract")
		return
	}
	writeJSON(w, http.StatusOK, contractToDTO(updated))
}

func (h *InventoryExtrasHandler) ActivateContract(w http.ResponseWriter, r *http.Request) {
	h.setContractStatus(w, r, entcontract.StatusActive, "inventory.contract.activated")
}

func (h *InventoryExtrasHandler) TerminateContract(w http.ResponseWriter, r *http.Request) {
	h.setContractStatus(w, r, entcontract.StatusTerminated, "inventory.contract.terminated")
}

func (h *InventoryExtrasHandler) setContractStatus(w http.ResponseWriter, r *http.Request, status entcontract.Status, eventType string) {
	tenantID, c, ok := h.loadContract(w, r)
	if !ok {
		return
	}
	updated, err := h.orm.Contract.UpdateOneID(c.ID).SetStatus(status).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update contract")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "contract", updated.ID, eventType, map[string]any{"id": updated.ID, "status": updated.Status})
	writeJSON(w, http.StatusOK, contractToDTO(updated))
}

// LinkContractOrder links a PurchaseOrder to a contract.
func (h *InventoryExtrasHandler) LinkContractOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, c, ok := h.loadContract(w, r)
	if !ok {
		return
	}
	var body struct {
		PurchaseOrderID uuid.UUID `json:"purchase_order_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PurchaseOrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "purchase_order_id is required")
		return
	}
	link, err := h.orm.ContractOrderLink.Create().
		SetTenantID(tenantID).SetContractID(c.ID).SetPurchaseOrderID(body.PurchaseOrderID).
		Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LINK_FAILED", "Failed to link order to contract")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": link.ID, "contract_id": c.ID, "purchase_order_id": body.PurchaseOrderID})
}

func (h *InventoryExtrasHandler) loadContract(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.Contract, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "contractID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid contract ID")
		return uuid.Nil, nil, false
	}
	c, err := h.orm.Contract.Query().Where(entcontract.ID(id), entcontract.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Contract not found")
		return uuid.Nil, nil, false
	}
	return tenantID, c, true
}
