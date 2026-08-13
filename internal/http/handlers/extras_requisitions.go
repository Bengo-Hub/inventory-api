package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entapproval "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
	entreqline "github.com/bengobox/inventory-service/internal/ent/requisitionline"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── Requisitions (procurement) ─────────────────────────────────────────────
// Migrated from ERP procurement.requisitions. Workflow:
// draft → submitted → procurement_review → approved → ordered (convert to PO) / rejected.

type requisitionLinePayload struct {
	ItemType       string     `json:"item_type"` // inventory | external | service
	ItemID         *uuid.UUID `json:"item_id"`
	Quantity       float64    `json:"quantity"`
	EstimatedPrice float64    `json:"estimated_price"`
	Description    string     `json:"description"`
	SupplierID     *uuid.UUID `json:"supplier_id"`
	Urgent         bool       `json:"urgent"`
	// External-item details (item_type=external)
	Specifications string `json:"specifications"`
	// Service details (item_type=service)
	ServiceDescription   string `json:"service_description"`
	ExpectedDeliverables string `json:"expected_deliverables"`
	Duration             string `json:"duration"`
}

type requisitionPayload struct {
	RequestType    string                   `json:"request_type"` // inventory | external_item | service
	Purpose        string                   `json:"purpose"`
	Priority       string                   `json:"priority"`
	OutletID       *uuid.UUID               `json:"outlet_id"`
	RequesterID    *uuid.UUID               `json:"requester_id"`
	ProjectID      *uuid.UUID               `json:"project_id"`
	RequiredByDate *time.Time               `json:"required_by_date"`
	Notes          string                   `json:"notes"`
	Lines          []requisitionLinePayload `json:"lines"`
}

type requisitionLineDTO struct {
	ID                   uuid.UUID  `json:"id"`
	ItemType             string     `json:"item_type"`
	ItemID               *uuid.UUID `json:"item_id"`
	Quantity             float64    `json:"quantity"`
	EstimatedPrice       *float64   `json:"estimated_price"`
	Description          string     `json:"description"`
	SupplierID           *uuid.UUID `json:"supplier_id"`
	Urgent               bool       `json:"urgent"`
	Specifications       string     `json:"specifications,omitempty"`
	ServiceDescription   string     `json:"service_description,omitempty"`
	ExpectedDeliverables string     `json:"expected_deliverables,omitempty"`
	Duration             string     `json:"duration,omitempty"`
}

type requisitionDTO struct {
	ID              uuid.UUID            `json:"id"`
	ReferenceNumber string               `json:"reference_number"`
	RequestType     string               `json:"request_type"`
	Purpose         string               `json:"purpose"`
	Priority        string               `json:"priority"`
	Status          string               `json:"status"`
	Notes           string               `json:"notes"`
	OutletID        *uuid.UUID           `json:"outlet_id"`
	RequiredByDate  *time.Time           `json:"required_by_date"`
	Lines           []requisitionLineDTO `json:"lines,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
}

func requisitionToDTO(rq *ent.Requisition, lines []*ent.RequisitionLine) requisitionDTO {
	dto := requisitionDTO{
		ID:              rq.ID,
		ReferenceNumber: rq.ReferenceNumber,
		RequestType:     string(rq.RequestType),
		Purpose:         rq.Purpose,
		Priority:        string(rq.Priority),
		Status:          string(rq.Status),
		Notes:           rq.Notes,
		OutletID:        rq.OutletID,
		RequiredByDate:  rq.RequiredByDate,
		CreatedAt:       rq.CreatedAt,
	}
	for _, l := range lines {
		ld := requisitionLineDTO{
			ID:                   l.ID,
			ItemType:             string(l.ItemType),
			ItemID:               l.ItemID,
			Quantity:             l.Quantity,
			Description:          l.Description,
			SupplierID:           l.SupplierID,
			Urgent:               l.Urgent,
			Specifications:       l.Specifications,
			ServiceDescription:   l.ServiceDescription,
			ExpectedDeliverables: l.ExpectedDeliverables,
			Duration:             l.Duration,
		}
		if l.EstimatedPrice != nil {
			ld.EstimatedPrice = l.EstimatedPrice
		}
		dto.Lines = append(dto.Lines, ld)
	}
	return dto
}

// publishOutbox writes a generic domain event to the outbox (reused by procurement/mfg/assets handlers).
func (h *InventoryExtrasHandler) publishOutbox(ctx context.Context, tenantID uuid.UUID, aggregateType string, aggregateID uuid.UUID, eventType string, payload map[string]any) {
	// The NATS subject the relay publishes on is Event.Subject() == AggregateType + "." +
	// EventType (shared-events convention). Callers that pass the FULL subject as eventType
	// (e.g. "inventory.purchase_return.approved") together with a resource aggregateType
	// ("purchase_return") would otherwise produce a DOUBLED subject
	// ("purchase_return.inventory.purchase_return.approved") that no subscriber listens on —
	// silently dropping the event. Normalize to the domain-prefixed convention (matching
	// writeOutboxEvent and the goods_receipt.posted publisher) so the subject is exactly the
	// intended "inventory.<resource>.<action>".
	if strings.HasPrefix(eventType, "inventory.") {
		aggregateType = "inventory"
		eventType = strings.TrimPrefix(eventType, "inventory.")
	}
	// Build the event the SAME way as the tx-based writeOutboxEvent (events.NewEvent) so every
	// inventory event — whichever path emits it — carries an identical envelope (Version,
	// Metadata, subject). No bespoke raw-struct construction.
	evt := events.NewEvent(eventType, aggregateType, aggregateID, tenantID, payload)
	raw, err := evt.ToJSON()
	if err != nil {
		h.log.Warn("outbox marshal failed", zap.String("event", eventType), zap.Error(err))
		return
	}
	if _, err := h.orm.OutboxEvent.Create().
		SetID(evt.ID).SetTenantID(tenantID).
		SetAggregateType(aggregateType).SetAggregateID(aggregateID.String()).
		SetEventType(eventType).SetPayload(json.RawMessage(raw)).
		SetStatus("PENDING").SetCreatedAt(evt.Timestamp).
		Save(ctx); err != nil {
		h.log.Warn("outbox write failed", zap.String("event", eventType), zap.Error(err))
	}
}

func (h *InventoryExtrasHandler) registerRequisitionRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change string) {
	featGate := authclient.RequireFeatureCode("requisitions")
	r.Get("/inventory/requisitions", h.ListRequisitions)
	r.Get("/inventory/requisitions/{reqID}", h.GetRequisition)
	r.With(featGate, perm(add)).Post("/inventory/requisitions", h.CreateRequisition)
	r.With(featGate, perm(change)).Post("/inventory/requisitions/{reqID}/submit", h.SubmitRequisition)
	r.With(featGate, perm(change)).Post("/inventory/requisitions/{reqID}/review", h.ReviewRequisition)
	r.With(featGate, perm(change)).Post("/inventory/requisitions/{reqID}/approve", h.ApproveRequisition)
	r.With(featGate, perm(change)).Post("/inventory/requisitions/{reqID}/reject", h.RejectRequisition)
	r.With(featGate, perm(add)).Post("/inventory/requisitions/{reqID}/convert-to-po", h.ConvertRequisitionToPO)
}

// ListRequisitions handles GET /inventory/requisitions.
//
//	@Summary      List requisitions
//	@Tags         Procurement
//	@Produce      json
//	@Param        status        query     string  false  "Filter by status"
//	@Param        request_type  query     string  false  "Filter by request type"
//	@Success      200           {object}  map[string]interface{}  "Paginated list of requisitionDTO"
//	@Failure      400           {object}  map[string]string
//	@Failure      500           {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions [get]
func (h *InventoryExtrasHandler) ListRequisitions(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.Requisition.Query().Where(entreq.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entreq.StatusEQ(entreq.Status(s)))
	}
	if rt := r.URL.Query().Get("request_type"); rt != "" {
		q = q.Where(entreq.RequestTypeEQ(entreq.RequestType(rt)))
	}
	if from, to, ok := parseCreatedAtRange(r); ok {
		q = q.Where(entreq.CreatedAtGTE(from), entreq.CreatedAtLTE(to))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entreq.FieldCreatedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list requisitions")
		return
	}
	result := make([]requisitionDTO, len(rows))
	for i, rq := range rows {
		result[i] = requisitionToDTO(rq, nil)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}

// GetRequisition handles GET /inventory/requisitions/{reqID}.
//
//	@Summary      Get a requisition with its lines
//	@Tags         Procurement
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {object}  requisitionDTO
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID} [get]
func (h *InventoryExtrasHandler) GetRequisition(w http.ResponseWriter, r *http.Request) {
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}
	lines, _ := h.orm.RequisitionLine.Query().
		Where(entreqline.RequisitionID(rq.ID), entreqline.TenantID(tenantID)).
		All(r.Context())
	writeJSON(w, http.StatusOK, requisitionToDTO(rq, lines))
}

// CreateRequisition handles POST /inventory/requisitions.
//
//	@Summary      Create a requisition with lines
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      requisitionPayload  true  "Requisition payload"
//	@Success      201   {object}  requisitionDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions [post]
func (h *InventoryExtrasHandler) CreateRequisition(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req requisitionPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	ref := "REQ-" + strings.ToUpper(uuid.New().String()[:8])
	if h.docSvc != nil {
		if n, e := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypeRequisition); e == nil && n != "" {
			ref = n
		}
	}
	create := h.orm.Requisition.Create().
		SetTenantID(tenantID).
		SetReferenceNumber(ref).
		SetPurpose(req.Purpose).
		SetNotes(req.Notes)
	if req.RequestType != "" {
		create = create.SetRequestType(entreq.RequestType(req.RequestType))
	}
	if req.Priority != "" {
		create = create.SetPriority(entreq.Priority(req.Priority))
	}
	if req.OutletID != nil {
		create = create.SetOutletID(*req.OutletID)
	}
	if req.RequesterID != nil {
		create = create.SetRequesterID(*req.RequesterID)
	}
	if req.ProjectID != nil {
		create = create.SetProjectID(*req.ProjectID)
	}
	if req.RequiredByDate != nil {
		create = create.SetRequiredByDate(*req.RequiredByDate)
	}
	rq, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create requisition failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create requisition")
		return
	}
	for _, l := range req.Lines {
		lc := h.orm.RequisitionLine.Create().
			SetTenantID(tenantID).
			SetRequisitionID(rq.ID).
			SetQuantity(l.Quantity).
			SetDescription(l.Description).
			SetUrgent(l.Urgent)
		if l.ItemType != "" {
			lc = lc.SetItemType(entreqline.ItemType(l.ItemType))
		}
		if l.ItemID != nil {
			lc = lc.SetItemID(*l.ItemID)
		}
		if l.SupplierID != nil {
			lc = lc.SetSupplierID(*l.SupplierID)
		}
		if l.EstimatedPrice > 0 {
			lc = lc.SetEstimatedPrice(l.EstimatedPrice)
		}
		// Type-specific detail fields (external item specs / service description etc.).
		// The schema already carries these columns, so persisting them is what makes the
		// type-gated requisition form round-trip correctly.
		if l.Specifications != "" {
			lc = lc.SetSpecifications(l.Specifications)
		}
		if l.ServiceDescription != "" {
			lc = lc.SetServiceDescription(l.ServiceDescription)
		}
		if l.ExpectedDeliverables != "" {
			lc = lc.SetExpectedDeliverables(l.ExpectedDeliverables)
		}
		if l.Duration != "" {
			lc = lc.SetDuration(l.Duration)
		}
		if _, err := lc.Save(r.Context()); err != nil {
			h.log.Warn("create requisition line failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "requisition", rq.ID, "inventory.requisition.created", map[string]any{
		"id": rq.ID, "reference_number": rq.ReferenceNumber, "status": rq.Status, "request_type": rq.RequestType,
	})
	lines, _ := h.orm.RequisitionLine.Query().Where(entreqline.RequisitionID(rq.ID)).All(r.Context())
	writeJSON(w, http.StatusCreated, requisitionToDTO(rq, lines))
}

// SubmitRequisition handles POST /inventory/requisitions/{reqID}/submit.
//
//	@Summary      Submit a requisition for review
//	@Tags         Procurement
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {object}  requisitionDTO
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/submit [post]
func (h *InventoryExtrasHandler) SubmitRequisition(w http.ResponseWriter, r *http.Request) {
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}
	updated, err := h.orm.Requisition.UpdateOneID(rq.ID).SetStatus(entreq.StatusSubmitted).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to submit requisition")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "requisition", updated.ID, "inventory.requisition.submitted", map[string]any{
		"id": updated.ID, "reference_number": updated.ReferenceNumber, "status": updated.Status,
	})
	// Kick off the approval matrix if a rule matches the estimated total.
	amount := h.requisitionAmount(r.Context(), rq.ID)
	var submittedBy *uuid.UUID
	if uid, ok := actingUserID(r); ok {
		submittedBy = &uid
	}
	if req, required, aerr := h.approvals().Submit(r.Context(), tenantID, "requisition", rq.ID, rq.ReferenceNumber, amount, submittedBy); aerr == nil && required {
		h.publishOutbox(r.Context(), tenantID, "approval_request", req.ID, "inventory.approval.submitted", map[string]any{
			"id": req.ID, "module": "requisition", "object_id": rq.ID, "amount": amount,
		})
	}
	writeJSON(w, http.StatusOK, requisitionToDTO(updated, nil))
}

// requisitionAmount sums estimated_price * quantity across a requisition's lines.
func (h *InventoryExtrasHandler) requisitionAmount(ctx context.Context, reqID uuid.UUID) float64 {
	lines, _ := h.orm.RequisitionLine.Query().Where(entreqline.RequisitionID(reqID)).All(ctx)
	var total float64
	for _, l := range lines {
		if l.EstimatedPrice != nil {
			total += *l.EstimatedPrice * float64(l.Quantity)
		}
	}
	return total
}

// ReviewRequisition handles POST /inventory/requisitions/{reqID}/review.
//
//	@Summary      Move a requisition into procurement review
//	@Tags         Procurement
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {object}  requisitionDTO
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/review [post]
func (h *InventoryExtrasHandler) ReviewRequisition(w http.ResponseWriter, r *http.Request) {
	h.transitionRequisition(w, r, entreq.StatusProcurementReview, "inventory.requisition.reviewing")
}

// ApproveRequisition handles POST /inventory/requisitions/{reqID}/approve.
//
//	@Summary      Approve a requisition
//	@Tags         Procurement
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {object}  requisitionDTO
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/approve [post]
func (h *InventoryExtrasHandler) ApproveRequisition(w http.ResponseWriter, r *http.Request) {
	h.decideRequisition(w, r, approvals.DecisionApprove, entreq.StatusApproved, "inventory.requisition.approved")
}

// RejectRequisition handles POST /inventory/requisitions/{reqID}/reject.
//
//	@Summary      Reject a requisition
//	@Tags         Procurement
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Success      200    {object}  requisitionDTO
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/reject [post]
func (h *InventoryExtrasHandler) RejectRequisition(w http.ResponseWriter, r *http.Request) {
	h.decideRequisition(w, r, approvals.DecisionReject, entreq.StatusRejected, "inventory.requisition.rejected")
}

// decideRequisition resolves an approve/reject on a requisition. When a live
// approval-matrix request exists it is driven through the engine (multi-step,
// role-gated); the requisition status flips only when the request reaches a
// terminal state. With no matrix configured it falls back to a direct transition.
func (h *InventoryExtrasHandler) decideRequisition(w http.ResponseWriter, r *http.Request, decision approvals.Decision, directStatus entreq.Status, directEvent string) {
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}
	latest, _ := h.approvals().Latest(r.Context(), tenantID, rq.ID)
	if latest != nil && latest.Status == entapproval.StatusPending {
		var body struct {
			Comment string `json:"comment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := h.applyApprovalDecision(w, r, tenantID, latest.ID, decision, body.Comment); !ok {
			return
		}
		fresh, err := h.orm.Requisition.Get(r.Context(), rq.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to reload requisition")
			return
		}
		writeJSON(w, http.StatusOK, requisitionToDTO(fresh, nil))
		return
	}
	// No approval matrix configured → direct transition (backward compatible).
	h.transitionRequisition(w, r, directStatus, directEvent)
}

func (h *InventoryExtrasHandler) transitionRequisition(w http.ResponseWriter, r *http.Request, status entreq.Status, eventType string) {
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}
	updated, err := h.orm.Requisition.UpdateOneID(rq.ID).SetStatus(status).Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update requisition")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "requisition", updated.ID, eventType, map[string]any{
		"id": updated.ID, "reference_number": updated.ReferenceNumber, "status": updated.Status,
	})
	// On approval, auto-raise purchase order(s) from the requisition lines (inventory,
	// external and service) so an approved request flows straight into procurement.
	if status == entreq.StatusApproved {
		h.autoCreatePOsFromRequisition(r.Context(), tenantID, updated)
		if fresh, ferr := h.orm.Requisition.Get(r.Context(), updated.ID); ferr == nil {
			updated = fresh
		}
	}
	writeJSON(w, http.StatusOK, requisitionToDTO(updated, nil))
}

// ConvertRequisitionToPO creates a PurchaseOrder (+ lines) from an approved requisition's inventory lines.
//
//	@Summary      Convert a requisition into a purchase order
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        reqID  path      string  true  "Requisition ID"
//	@Param        body   body      object  true  "Supplier, warehouse and currency"
//	@Success      201    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Failure      500    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/requisitions/{reqID}/convert-to-po [post]
func (h *InventoryExtrasHandler) ConvertRequisitionToPO(w http.ResponseWriter, r *http.Request) {
	tenantID, rq, ok := h.loadRequisition(w, r)
	if !ok {
		return
	}

	// Approval already auto-raises the PO(s); block a second conversion so lines aren't
	// double-ordered. Manual conversion remains the fallback when auto couldn't resolve a
	// supplier/warehouse (status stays "approved").
	if rq.Status == entreq.StatusOrdered {
		writeError(w, http.StatusConflict, "ALREADY_ORDERED", "This requisition has already been converted to a purchase order")
		return
	}

	// Service requisitions never become purchase orders — they spawn a
	// ServiceDelivery record per service line (the procurement counterpart for
	// consultancy/labour). No supplier/warehouse body is required.
	if rq.RequestType == entreq.RequestTypeService {
		svcLines, _ := h.orm.RequisitionLine.Query().
			Where(entreqline.RequisitionID(rq.ID), entreqline.ItemTypeEQ(entreqline.ItemTypeService)).
			All(r.Context())
		created := 0
		for _, l := range svcLines {
			// Service cost = estimated unit price × quantity (services aren't stocked; this is the payable).
			amount := 0.0
			if l.EstimatedPrice != nil {
				amount = *l.EstimatedPrice * float64(l.Quantity)
			}
			c := h.orm.ServiceDelivery.Create().SetTenantID(tenantID).
				SetRequisitionLineID(l.ID).SetDeliverables(l.ExpectedDeliverables).SetAmount(amount)
			if l.SupplierID != nil {
				c = c.SetProviderID(*l.SupplierID)
			}
			sd, err := c.Save(r.Context())
			if err != nil {
				h.log.Warn("convert requisition: create service delivery failed", zap.Error(err))
				continue
			}
			created++
			// Treasury posts a payable/expense for the service when an amount + provider are known.
			if amount > 0 && l.SupplierID != nil {
				h.publishOutbox(r.Context(), tenantID, "service_delivery", sd.ID, "inventory.service_delivery.created", map[string]any{
					"tenant_id": tenantID, "service_delivery_id": sd.ID.String(),
					"requisition_id": rq.ID.String(), "reference_number": rq.ReferenceNumber,
					"supplier_id": l.SupplierID.String(), "amount": amount, "currency": "KES",
					"description": l.ServiceDescription,
				})
			}
		}
		_, _ = h.orm.Requisition.UpdateOneID(rq.ID).SetStatus(entreq.StatusOrdered).Save(r.Context())
		h.publishOutbox(r.Context(), tenantID, "requisition", rq.ID, "inventory.requisition.ordered", map[string]any{
			"id": rq.ID, "reference_number": rq.ReferenceNumber, "service_deliveries": created,
		})
		writeJSON(w, http.StatusCreated, map[string]any{"requisition_id": rq.ID, "service_deliveries_created": created})
		return
	}

	var body struct {
		SupplierID                uuid.UUID `json:"supplier_id"`
		WarehouseID               uuid.UUID `json:"warehouse_id"`
		Currency                  string    `json:"currency"`
		PayTermDays               *int      `json:"pay_term_days"`
		AdditionalShippingCharges float64   `json:"additional_shipping_charges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if body.SupplierID == uuid.Nil || body.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "supplier_id and warehouse_id are required")
		return
	}
	lines, _ := h.orm.RequisitionLine.Query().
		Where(entreqline.RequisitionID(rq.ID), entreqline.ItemTypeEQ(entreqline.ItemTypeInventory)).
		All(r.Context())
	if len(lines) == 0 {
		writeError(w, http.StatusBadRequest, "NO_LINES", "Requisition has no inventory lines to order")
		return
	}
	poNumber := "PO-" + strings.ToUpper(uuid.New().String()[:8])
	if h.docSvc != nil {
		if n, e := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypePurchaseOrder); e == nil && n != "" {
			poNumber = n
		}
	}
	currency := body.Currency
	if currency == "" {
		currency = "KES"
	}
	var total float64
	po, err := h.orm.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(body.SupplierID).
		SetWarehouseID(body.WarehouseID).
		SetPoNumber(poNumber).
		SetCurrency(currency).
		SetRequisitionID(rq.ID).
		SetNillableProjectID(rq.ProjectID). // inherit the project so treasury attributes the cost
		SetNillablePayTermDays(body.PayTermDays).
		SetAdditionalShippingCharges(body.AdditionalShippingCharges).
		SetNotes(fmt.Sprintf("From requisition %s", rq.ReferenceNumber)).
		Save(r.Context())
	if err != nil {
		h.log.Error("convert requisition: create PO failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order")
		return
	}
	for _, l := range lines {
		if l.ItemID == nil {
			continue
		}
		var price float64
		if l.EstimatedPrice != nil {
			price = *l.EstimatedPrice
		}
		lineTotal := price * float64(l.Quantity)
		total += lineTotal
		if _, err := h.orm.PurchaseOrderLine.Create().
			SetPoID(po.ID).
			SetItemID(*l.ItemID).
			SetQuantityOrdered(l.Quantity).
			SetUnitPrice(price).
			SetTotalPrice(lineTotal).
			Save(r.Context()); err != nil {
			h.log.Warn("convert requisition: create PO line failed", zap.Error(err))
		}
	}
	po, _ = h.orm.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(total).Save(r.Context())
	_, _ = h.orm.Requisition.UpdateOneID(rq.ID).SetStatus(entreq.StatusOrdered).Save(r.Context())
	h.publishOutbox(r.Context(), tenantID, "purchase_order", po.ID, "inventory.purchase_order.created", map[string]any{
		"id": po.ID, "po_number": po.PoNumber, "supplier_id": body.SupplierID, "total_amount": total, "from_requisition_id": rq.ID,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"purchase_order_id": po.ID, "po_number": po.PoNumber, "total_amount": total})
}

func (h *InventoryExtrasHandler) loadRequisition(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.Requisition, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "reqID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid requisition ID")
		return uuid.Nil, nil, false
	}
	rq, err := h.orm.Requisition.Query().Where(entreq.ID(reqID), entreq.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Requisition not found")
		return uuid.Nil, nil, false
	}
	return tenantID, rq, true
}
