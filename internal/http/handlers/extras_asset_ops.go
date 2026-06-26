package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entasset "github.com/bengobox/inventory-service/internal/ent/asset"
	entaudit "github.com/bengobox/inventory-service/internal/ent/assetaudit"
	entdisp "github.com/bengobox/inventory-service/internal/ent/assetdisposal"
	entins "github.com/bengobox/inventory-service/internal/ent/assetinsurance"
	entmaint "github.com/bengobox/inventory-service/internal/ent/assetmaintenance"
	entresv "github.com/bengobox/inventory-service/internal/ent/assetreservation"
	enttrf "github.com/bengobox/inventory-service/internal/ent/assettransfer"
)

// ─── Asset operations: maintenance, transfer, disposal, insurance, audit, reservation ──

func (h *InventoryExtrasHandler) registerAssetOpsRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	r.Get("/inventory/assets/{assetID}/maintenance", h.ListAssetMaintenance)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/maintenance", h.CreateAssetMaintenance)
	r.With(perm(change)).Post("/inventory/asset-maintenance/{recID}/complete", h.CompleteAssetMaintenance)

	r.Get("/inventory/assets/{assetID}/transfers", h.ListAssetTransfers)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/transfers", h.CreateAssetTransfer)
	r.With(perm(change)).Post("/inventory/asset-transfers/{recID}/approve", h.ApproveAssetTransfer)
	r.With(perm(change)).Post("/inventory/asset-transfers/{recID}/complete", h.CompleteAssetTransfer)

	r.Get("/inventory/assets/{assetID}/disposals", h.ListAssetDisposals)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/disposals", h.CreateAssetDisposal)
	r.With(perm(change)).Post("/inventory/asset-disposals/{recID}/complete", h.CompleteAssetDisposal)

	r.Get("/inventory/assets/{assetID}/insurance", h.ListAssetInsurance)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/insurance", h.CreateAssetInsurance)

	r.Get("/inventory/assets/{assetID}/audits", h.ListAssetAudits)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/audits", h.CreateAssetAudit)
	r.With(perm(change)).Post("/inventory/asset-audits/{recID}/complete", h.CompleteAssetAudit)

	r.Get("/inventory/assets/{assetID}/reservations", h.ListAssetReservations)
	r.With(perm(add)).Post("/inventory/assets/{assetID}/reservations", h.CreateAssetReservation)
}

// helper: resolve tenant + asset_id from path (asset existence validated lightly)
func (h *InventoryExtrasHandler) assetCtx(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, uuid.Nil, false
	}
	assetID, err := uuid.Parse(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid asset ID")
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, assetID, true
}

// --- Maintenance ---

// ListAssetMaintenance handles GET /inventory/assets/{assetID}/maintenance.
//
//	@Summary      List maintenance records for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/maintenance [get]
func (h *InventoryExtrasHandler) ListAssetMaintenance(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetMaintenance.Query().Where(entmaint.TenantID(tenantID), entmaint.AssetID(assetID)).Order(ent.Desc(entmaint.FieldScheduledDate)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetMaintenance handles POST /inventory/assets/{assetID}/maintenance.
//
//	@Summary      Schedule a maintenance record for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Maintenance payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/maintenance [post]
func (h *InventoryExtrasHandler) CreateAssetMaintenance(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		Type          string     `json:"maintenance_type"`
		ScheduledDate *time.Time `json:"scheduled_date"`
		PerformedBy   string     `json:"performed_by"`
		Cost          float64    `json:"cost"`
		Description   string     `json:"description"`
		Priority      string     `json:"priority"`
		NextDate      *time.Time `json:"next_maintenance_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	c := h.orm.AssetMaintenance.Create().SetTenantID(tenantID).SetAssetID(assetID).
		SetPerformedBy(b.PerformedBy).SetCost(b.Cost).SetDescription(b.Description)
	if b.Type != "" {
		c = c.SetMaintenanceType(entmaint.MaintenanceType(b.Type))
	}
	if b.Priority != "" {
		c = c.SetPriority(entmaint.Priority(b.Priority))
	}
	if b.ScheduledDate != nil {
		c = c.SetScheduledDate(*b.ScheduledDate)
	} else {
		c = c.SetScheduledDate(time.Now().UTC())
	}
	if b.NextDate != nil {
		c = c.SetNextMaintenanceDate(*b.NextDate)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create maintenance record")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// CompleteAssetMaintenance handles POST /inventory/asset-maintenance/{recID}/complete.
//
//	@Summary      Complete a maintenance record
//	@Tags         Assets
//	@Produce      json
//	@Param        recID  path      string  true  "Maintenance record ID"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-maintenance/{recID}/complete [post]
func (h *InventoryExtrasHandler) CompleteAssetMaintenance(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "recID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid record ID")
		return
	}
	rec, err := h.orm.AssetMaintenance.Query().Where(entmaint.ID(id), entmaint.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Maintenance record not found")
		return
	}
	if !h.gateApproval(w, r, tenantID, "asset_maintenance", rec.ID, "", rec.Cost) {
		return
	}
	now := time.Now().UTC()
	updated, err := h.orm.AssetMaintenance.UpdateOneID(rec.ID).SetStatus(entmaint.StatusCompleted).SetCompletedDate(now).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete maintenance")
		return
	}
	upd := h.orm.Asset.UpdateOneID(rec.AssetID).SetLastMaintenance(now)
	if rec.NextMaintenanceDate != nil {
		upd = upd.SetNextMaintenance(*rec.NextMaintenanceDate)
	}
	_, _ = upd.Save(r.Context())
	writeJSON(w, http.StatusOK, updated)
}

// --- Transfers ---

// ListAssetTransfers handles GET /inventory/assets/{assetID}/transfers.
//
//	@Summary      List transfers for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/transfers [get]
func (h *InventoryExtrasHandler) ListAssetTransfers(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetTransfer.Query().Where(enttrf.TenantID(tenantID), enttrf.AssetID(assetID)).Order(ent.Desc(enttrf.FieldTransferDate)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetTransfer handles POST /inventory/assets/{assetID}/transfers.
//
//	@Summary      Create a transfer for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Transfer payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/transfers [post]
func (h *InventoryExtrasHandler) CreateAssetTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		FromLocation string     `json:"from_location"`
		ToLocation   string     `json:"to_location"`
		ToUser       *uuid.UUID `json:"to_user"`
		Reason       string     `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	c := h.orm.AssetTransfer.Create().SetTenantID(tenantID).SetAssetID(assetID).
		SetFromLocation(b.FromLocation).SetToLocation(b.ToLocation).SetReason(b.Reason)
	if b.ToUser != nil {
		c = c.SetToUser(*b.ToUser)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create transfer")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// CompleteAssetTransfer handles POST /inventory/asset-transfers/{recID}/complete.
//
//	@Summary      Complete an asset transfer
//	@Tags         Assets
//	@Produce      json
//	@Param        recID  path      string  true  "Transfer record ID"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-transfers/{recID}/complete [post]
func (h *InventoryExtrasHandler) CompleteAssetTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "recID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid record ID")
		return
	}
	rec, err := h.orm.AssetTransfer.Query().Where(enttrf.ID(id), enttrf.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Transfer not found")
		return
	}
	// A transfer must be approved (or in transit) before it can be completed.
	if rec.Status != enttrf.StatusApproved && rec.Status != enttrf.StatusInTransit {
		writeError(w, http.StatusBadRequest, "NOT_APPROVED", "Transfer must be approved before it can be completed")
		return
	}
	var assetVal float64
	if a, e := h.orm.Asset.Get(r.Context(), rec.AssetID); e == nil {
		assetVal = a.CurrentValue
	}
	if !h.gateApproval(w, r, tenantID, "asset_transfer", rec.ID, "", assetVal) {
		return
	}
	updated, err := h.orm.AssetTransfer.UpdateOneID(rec.ID).SetStatus(enttrf.StatusCompleted).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete transfer")
		return
	}
	upd := h.orm.Asset.UpdateOneID(rec.AssetID)
	if rec.ToLocation != "" {
		upd = upd.SetLocation(rec.ToLocation)
	}
	if rec.ToUser != nil {
		upd = upd.SetCustodianID(*rec.ToUser)
	}
	_, _ = upd.Save(r.Context())
	writeJSON(w, http.StatusOK, updated)
}

// ApproveAssetTransfer handles POST /inventory/asset-transfers/{recID}/approve.
//
//	@Summary      Approve a pending asset transfer
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        recID  path      string  true   "Transfer record ID"
//	@Param        body   body      object  false  "approved_by"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-transfers/{recID}/approve [post]
func (h *InventoryExtrasHandler) ApproveAssetTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "recID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid record ID")
		return
	}
	rec, err := h.orm.AssetTransfer.Query().Where(enttrf.ID(id), enttrf.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Transfer not found")
		return
	}
	if rec.Status != enttrf.StatusPending {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "Only pending transfers can be approved")
		return
	}
	var b struct {
		ApprovedBy *uuid.UUID `json:"approved_by"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	upd := h.orm.AssetTransfer.UpdateOneID(rec.ID).SetStatus(enttrf.StatusApproved)
	if b.ApprovedBy != nil {
		upd = upd.SetApprovedBy(*b.ApprovedBy)
	}
	updated, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to approve transfer")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "asset", rec.AssetID, "inventory.asset.transfer_approved", map[string]any{"asset_id": rec.AssetID, "transfer_id": rec.ID})
	writeJSON(w, http.StatusOK, updated)
}

// --- Disposals ---

// ListAssetDisposals handles GET /inventory/assets/{assetID}/disposals.
//
//	@Summary      List disposals for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/disposals [get]
func (h *InventoryExtrasHandler) ListAssetDisposals(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetDisposal.Query().Where(entdisp.TenantID(tenantID), entdisp.AssetID(assetID)).Order(ent.Desc(entdisp.FieldDisposalDate)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetDisposal handles POST /inventory/assets/{assetID}/disposals.
//
//	@Summary      Create a disposal for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Disposal payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/disposals [post]
func (h *InventoryExtrasHandler) CreateAssetDisposal(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		Method string  `json:"disposal_method"`
		Value  float64 `json:"disposal_value"`
		Reason string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	c := h.orm.AssetDisposal.Create().SetTenantID(tenantID).SetAssetID(assetID).
		SetDisposalDate(time.Now().UTC()).SetDisposalValue(b.Value).SetReason(b.Reason)
	if b.Method != "" {
		c = c.SetDisposalMethod(entdisp.DisposalMethod(b.Method))
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create disposal")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// CompleteAssetDisposal handles POST /inventory/asset-disposals/{recID}/complete.
//
//	@Summary      Complete an asset disposal
//	@Tags         Assets
//	@Produce      json
//	@Param        recID  path      string  true  "Disposal record ID"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-disposals/{recID}/complete [post]
func (h *InventoryExtrasHandler) CompleteAssetDisposal(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "recID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid record ID")
		return
	}
	rec, err := h.orm.AssetDisposal.Query().Where(entdisp.ID(id), entdisp.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Disposal not found")
		return
	}
	if !h.gateApproval(w, r, tenantID, "asset_disposal", rec.ID, "", rec.DisposalValue) {
		return
	}
	updated, err := h.orm.AssetDisposal.UpdateOneID(rec.ID).SetStatus(entdisp.StatusCompleted).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete disposal")
		return
	}
	_, _ = h.orm.Asset.UpdateOneID(rec.AssetID).SetStatus(entasset.StatusDisposed).SetCurrentValue(0).SetBookValue(0).Save(r.Context())
	h.publishOutbox(r.Context(), tenantID, "asset", rec.AssetID, "inventory.asset.disposed", map[string]any{
		"asset_id":    rec.AssetID,
		"disposal_id": rec.ID,
		"method":      rec.DisposalMethod,
		// proceeds drive the treasury capital gain/loss (proceeds − tax WDV).
		"proceeds": rec.DisposalValue,
	})
	writeJSON(w, http.StatusOK, updated)
}

// --- Insurance ---

// ListAssetInsurance handles GET /inventory/assets/{assetID}/insurance.
//
//	@Summary      List insurance policies for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/insurance [get]
func (h *InventoryExtrasHandler) ListAssetInsurance(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetInsurance.Query().Where(entins.TenantID(tenantID), entins.AssetID(assetID)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetInsurance handles POST /inventory/assets/{assetID}/insurance.
//
//	@Summary      Create an insurance policy for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Insurance payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/insurance [post]
func (h *InventoryExtrasHandler) CreateAssetInsurance(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		PolicyNumber   string     `json:"policy_number"`
		Provider       string     `json:"provider"`
		PolicyType     string     `json:"policy_type"`
		CoverageAmount float64    `json:"coverage_amount"`
		PremiumAmount  float64    `json:"premium_amount"`
		StartDate      *time.Time `json:"start_date"`
		EndDate        *time.Time `json:"end_date"`
		Deductible     float64    `json:"deductible"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.PolicyNumber == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "policy_number is required")
		return
	}
	c := h.orm.AssetInsurance.Create().SetTenantID(tenantID).SetAssetID(assetID).
		SetPolicyNumber(b.PolicyNumber).SetProvider(b.Provider).SetPolicyType(b.PolicyType).
		SetCoverageAmount(b.CoverageAmount).SetPremiumAmount(b.PremiumAmount).SetDeductible(b.Deductible)
	if b.StartDate != nil {
		c = c.SetStartDate(*b.StartDate)
	} else {
		c = c.SetStartDate(time.Now().UTC())
	}
	if b.EndDate != nil {
		c = c.SetEndDate(*b.EndDate)
	} else {
		c = c.SetEndDate(time.Now().UTC().AddDate(1, 0, 0))
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create insurance policy")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// --- Audits ---

// ListAssetAudits handles GET /inventory/assets/{assetID}/audits.
//
//	@Summary      List audits for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/audits [get]
func (h *InventoryExtrasHandler) ListAssetAudits(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetAudit.Query().Where(entaudit.TenantID(tenantID), entaudit.AssetID(assetID)).Order(ent.Desc(entaudit.FieldAuditDate)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetAudit handles POST /inventory/assets/{assetID}/audits.
//
//	@Summary      Record an audit for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Audit payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/audits [post]
func (h *InventoryExtrasHandler) CreateAssetAudit(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		AuditorID         *uuid.UUID `json:"auditor_id"`
		LocationVerified  string     `json:"location_verified"`
		ConditionVerified string     `json:"condition_verified"`
		Discrepancies     string     `json:"discrepancies"`
		Recommendations   string     `json:"recommendations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	// An audit starts in progress; it is finalised via CompleteAssetAudit. (Previously
	// it was created as 'completed', which skipped the actual verification step.)
	c := h.orm.AssetAudit.Create().SetTenantID(tenantID).SetAssetID(assetID).SetAuditDate(time.Now().UTC()).
		SetLocationVerified(b.LocationVerified).SetConditionVerified(b.ConditionVerified).
		SetDiscrepancies(b.Discrepancies).SetRecommendations(b.Recommendations).SetStatus(entaudit.StatusInProgress)
	if b.AuditorID != nil {
		c = c.SetAuditorID(*b.AuditorID)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create audit")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// CompleteAssetAudit handles POST /inventory/asset-audits/{recID}/complete.
//
//	@Summary      Complete an asset audit
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        recID  path      string  true  "Audit record ID"
//	@Param        body   body      object  false  "Optional verification results"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-audits/{recID}/complete [post]
func (h *InventoryExtrasHandler) CompleteAssetAudit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "recID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid record ID")
		return
	}
	rec, err := h.orm.AssetAudit.Query().Where(entaudit.ID(id), entaudit.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Audit not found")
		return
	}
	// Optional verification results captured at completion time.
	var b struct {
		LocationVerified  *string    `json:"location_verified"`
		ConditionVerified *string    `json:"condition_verified"`
		Discrepancies     *string    `json:"discrepancies"`
		Recommendations   *string    `json:"recommendations"`
		NextAuditDate     *time.Time `json:"next_audit_date"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	upd := h.orm.AssetAudit.UpdateOneID(rec.ID).SetStatus(entaudit.StatusCompleted)
	if b.LocationVerified != nil {
		upd = upd.SetLocationVerified(*b.LocationVerified)
	}
	if b.ConditionVerified != nil {
		upd = upd.SetConditionVerified(*b.ConditionVerified)
	}
	if b.Discrepancies != nil {
		upd = upd.SetDiscrepancies(*b.Discrepancies)
	}
	if b.Recommendations != nil {
		upd = upd.SetRecommendations(*b.Recommendations)
	}
	if b.NextAuditDate != nil {
		upd = upd.SetNextAuditDate(*b.NextAuditDate)
	}
	updated, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to complete audit")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// --- Reservations ---

// ListAssetReservations handles GET /inventory/assets/{assetID}/reservations.
//
//	@Summary      List reservations for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {array}   map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/reservations [get]
func (h *InventoryExtrasHandler) ListAssetReservations(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	rows, _ := h.orm.AssetReservation.Query().Where(entresv.TenantID(tenantID), entresv.AssetID(assetID)).Order(ent.Desc(entresv.FieldCreatedAt)).All(r.Context())
	writeJSON(w, http.StatusOK, rows)
}

// CreateAssetReservation handles POST /inventory/assets/{assetID}/reservations.
//
//	@Summary      Create a reservation for an asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Param        body     body      object  true  "Reservation payload"
//	@Success      201      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      500      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/reservations [post]
func (h *InventoryExtrasHandler) CreateAssetReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, assetID, ok := h.assetCtx(w, r)
	if !ok {
		return
	}
	var b struct {
		ReservedBy uuid.UUID  `json:"reserved_by"`
		StartDate  *time.Time `json:"start_date"`
		EndDate    *time.Time `json:"end_date"`
		Purpose    string     `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.ReservedBy == uuid.Nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "reserved_by is required")
		return
	}
	c := h.orm.AssetReservation.Create().SetTenantID(tenantID).SetAssetID(assetID).SetReservedBy(b.ReservedBy).SetPurpose(b.Purpose)
	if b.StartDate != nil {
		c = c.SetStartDate(*b.StartDate)
	} else {
		c = c.SetStartDate(time.Now().UTC())
	}
	if b.EndDate != nil {
		c = c.SetEndDate(*b.EndDate)
	} else {
		c = c.SetEndDate(time.Now().UTC().AddDate(0, 0, 1))
	}
	row, err := c.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create reservation")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

var _ = ent.Asc // keep ent import used if all queries inlined
