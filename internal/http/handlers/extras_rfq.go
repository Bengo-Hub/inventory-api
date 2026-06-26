package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entcontract "github.com/bengobox/inventory-service/internal/ent/contract"
	entreqline "github.com/bengobox/inventory-service/internal/ent/requisitionline"
	entrfq "github.com/bengobox/inventory-service/internal/ent/rfq"
	entrfqaward "github.com/bengobox/inventory-service/internal/ent/rfqaward"
	entrfqline "github.com/bengobox/inventory-service/internal/ent/rfqline"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	entsupresp "github.com/bengobox/inventory-service/internal/ent/supplierresponse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── DTOs ────────────────────────────────────────────────────────────────────

type rfqLineDTO struct {
	ID          uuid.UUID  `json:"id"`
	ItemID      *uuid.UUID `json:"item_id,omitempty"`
	ItemName    string     `json:"item_name,omitempty"`
	Description string     `json:"description"`
	Quantity    float64    `json:"quantity"`
	UOM         string     `json:"uom,omitempty"`
}

type quotedItemDTO struct {
	RFQLineID    uuid.UUID `json:"rfq_line_id"`
	UnitPrice    float64   `json:"unit_price"`
	LeadTimeDays int       `json:"lead_time_days"`
	Available    bool      `json:"available"`
	Notes        string    `json:"notes,omitempty"`
}

type supplierResponseDTO struct {
	ID           uuid.UUID       `json:"id"`
	SupplierID   uuid.UUID       `json:"supplier_id"`
	SupplierName string          `json:"supplier_name,omitempty"`
	Status       string          `json:"status"`
	Currency     string          `json:"currency"`
	Notes        string          `json:"notes,omitempty"`
	SubmittedAt  *time.Time      `json:"submitted_at,omitempty"`
	QuotedItems  []quotedItemDTO `json:"quoted_items"`
	Total        float64         `json:"total"`
}

type rfqAwardDTO struct {
	ID           uuid.UUID  `json:"id"`
	RFQLineID    uuid.UUID  `json:"rfq_line_id"`
	SupplierID   uuid.UUID  `json:"supplier_id"`
	SupplierName string     `json:"supplier_name,omitempty"`
	UnitPrice    float64    `json:"unit_price"`
	Quantity     float64    `json:"quantity"`
	POID         *uuid.UUID `json:"po_id,omitempty"`
}

type rfqDTO struct {
	ID            uuid.UUID             `json:"id"`
	RFQNumber     string                `json:"rfq_number"`
	Title         string                `json:"title"`
	Status        string                `json:"status"`
	RequisitionID *uuid.UUID            `json:"requisition_id,omitempty"`
	ProjectID     *uuid.UUID            `json:"project_id,omitempty"`
	WarehouseID   *uuid.UUID            `json:"warehouse_id,omitempty"`
	Notes         string                `json:"notes,omitempty"`
	DueDate       *time.Time            `json:"due_date,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
	Lines         []rfqLineDTO          `json:"lines,omitempty"`
	Responses     []supplierResponseDTO `json:"responses,omitempty"`
	Awards        []rfqAwardDTO         `json:"awards,omitempty"`
}

// ─── Name resolution helpers ─────────────────────────────────────────────────

func (h *InventoryExtrasHandler) supplierName(ctx context.Context, tenantID, id uuid.UUID, cache map[uuid.UUID]string) string {
	if cache != nil {
		if n, ok := cache[id]; ok {
			return n
		}
	}
	s, err := h.orm.Supplier.Get(ctx, id)
	name := ""
	if err == nil && s.TenantID == tenantID {
		name = s.Name
	}
	if cache != nil {
		cache[id] = name
	}
	return name
}

func (h *InventoryExtrasHandler) itemName(ctx context.Context, tenantID, id uuid.UUID, cache map[uuid.UUID]string) string {
	if cache != nil {
		if n, ok := cache[id]; ok {
			return n
		}
	}
	it, err := h.orm.Item.Get(ctx, id)
	name := ""
	if err == nil && it.TenantID == tenantID {
		name = it.Name
	}
	if cache != nil {
		cache[id] = name
	}
	return name
}

func (h *InventoryExtrasHandler) rfqToDTO(ctx context.Context, tenantID uuid.UUID, r *ent.RFQ, withChildren bool) rfqDTO {
	dto := rfqDTO{
		ID: r.ID, RFQNumber: r.RfqNumber, Title: r.Title, Status: string(r.Status),
		RequisitionID: r.RequisitionID, ProjectID: r.ProjectID, WarehouseID: r.WarehouseID, Notes: r.Notes,
		DueDate: r.DueDate, CreatedAt: r.CreatedAt,
	}
	if !withChildren {
		return dto
	}
	itemCache := map[uuid.UUID]string{}
	supCache := map[uuid.UUID]string{}
	for _, l := range r.Edges.Lines {
		ld := rfqLineDTO{ID: l.ID, ItemID: l.ItemID, Description: l.Description, Quantity: l.Quantity, UOM: l.Uom}
		if l.ItemID != nil {
			ld.ItemName = h.itemName(ctx, tenantID, *l.ItemID, itemCache)
		}
		dto.Lines = append(dto.Lines, ld)
	}
	for _, resp := range r.Edges.Responses {
		rd := supplierResponseDTO{
			ID: resp.ID, SupplierID: resp.SupplierID, Status: string(resp.Status),
			Currency: resp.Currency, Notes: resp.Notes, SubmittedAt: resp.SubmittedAt,
			SupplierName: h.supplierName(ctx, tenantID, resp.SupplierID, supCache),
		}
		for _, qi := range resp.QuotedItems {
			rd.QuotedItems = append(rd.QuotedItems, quotedItemDTO{
				RFQLineID: qi.RFQLineID, UnitPrice: qi.UnitPrice, LeadTimeDays: qi.LeadTimeDays,
				Available: qi.Available, Notes: qi.Notes,
			})
			rd.Total += qi.UnitPrice * float64(h.qtyForLine(r, qi.RFQLineID))
		}
		dto.Responses = append(dto.Responses, rd)
	}
	for _, a := range r.Edges.Awards {
		dto.Awards = append(dto.Awards, rfqAwardDTO{
			ID: a.ID, RFQLineID: a.RfqLineID, SupplierID: a.SupplierID,
			SupplierName: h.supplierName(ctx, tenantID, a.SupplierID, supCache),
			UnitPrice:    a.UnitPrice, Quantity: a.Quantity, POID: a.PoID,
		})
	}
	return dto
}

// qtyForLine returns the requested quantity for a line id from a loaded RFQ.
func (h *InventoryExtrasHandler) qtyForLine(r *ent.RFQ, lineID uuid.UUID) float64 {
	for _, l := range r.Edges.Lines {
		if l.ID == lineID {
			return l.Quantity
		}
	}
	return 0
}

func (h *InventoryExtrasHandler) loadRFQFull(ctx context.Context, tenantID, rfqID uuid.UUID) (*ent.RFQ, error) {
	return h.orm.RFQ.Query().
		Where(entrfq.ID(rfqID), entrfq.TenantID(tenantID)).
		WithLines().WithResponses().WithAwards().
		Only(ctx)
}

// ─── Routes ──────────────────────────────────────────────────────────────────

func (h *InventoryExtrasHandler) registerRFQRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change, del string) {
	r.Get("/inventory/rfqs", h.ListRFQs)
	r.Get("/inventory/rfqs/{rfqID}", h.GetRFQ)
	r.Get("/inventory/rfqs/{rfqID}/comparison", h.RFQComparison)
	r.With(perm(add)).Post("/inventory/rfqs", h.CreateRFQ)
	r.With(perm(change)).Put("/inventory/rfqs/{rfqID}", h.UpdateRFQ)
	r.With(perm(del)).Delete("/inventory/rfqs/{rfqID}", h.DeleteRFQ)
	r.With(perm(change)).Post("/inventory/rfqs/{rfqID}/suppliers", h.InviteRFQSuppliers)
	r.With(perm(change)).Delete("/inventory/rfqs/{rfqID}/responses/{responseID}", h.RemoveRFQSupplier)
	r.With(perm(change)).Post("/inventory/rfqs/{rfqID}/send", h.SendRFQ)
	r.With(perm(change)).Put("/inventory/rfqs/{rfqID}/responses/{responseID}/quote", h.CaptureRFQQuote)
	r.With(perm(change)).Post("/inventory/rfqs/{rfqID}/responses/{responseID}/decline", h.DeclineRFQResponse)
	r.With(perm(change)).Post("/inventory/rfqs/{rfqID}/award", h.AwardRFQ)
	r.With(perm(add)).Post("/inventory/rfqs/{rfqID}/convert-to-po", h.ConvertRFQToPOs)
}

// ─── Create / list / get / update / delete ───────────────────────────────────

type rfqLinePayload struct {
	ItemID      *uuid.UUID `json:"item_id"`
	Description string     `json:"description"`
	Quantity    float64    `json:"quantity"`
	UOM         string     `json:"uom"`
}

type createRFQPayload struct {
	Title         string           `json:"title"`
	RequisitionID *uuid.UUID       `json:"requisition_id"`
	WarehouseID   *uuid.UUID       `json:"warehouse_id"`
	Notes         string           `json:"notes"`
	DueDate       *time.Time       `json:"due_date"`
	Lines         []rfqLinePayload `json:"lines"`
}

// CreateRFQ handles POST /inventory/rfqs. If requisition_id is given and no lines
// are supplied, the requisition's inventory lines are copied in.
func (h *InventoryExtrasHandler) CreateRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var p createRFQPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	rfqNumber := "RFQ-" + strings.ToUpper(uuid.New().String()[:8])
	if h.docSvc != nil {
		if n, e := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypeRFQ); e == nil && n != "" {
			rfqNumber = n
		}
	}
	create := h.orm.RFQ.Create().
		SetTenantID(tenantID).
		SetRfqNumber(rfqNumber).
		SetTitle(p.Title).
		SetNotes(p.Notes)
	if p.RequisitionID != nil {
		create = create.SetRequisitionID(*p.RequisitionID)
		// Inherit the originating requisition's project so the chain stays project-attributed.
		if req, rerr := h.orm.Requisition.Get(r.Context(), *p.RequisitionID); rerr == nil && req.ProjectID != nil {
			create = create.SetProjectID(*req.ProjectID)
		}
	}
	if p.WarehouseID != nil {
		create = create.SetWarehouseID(*p.WarehouseID)
	}
	if p.DueDate != nil {
		create = create.SetDueDate(*p.DueDate)
	}
	if uid, ok := actingUserID(r); ok {
		create = create.SetCreatedBy(uid)
	}
	rfq, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create RFQ failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create RFQ")
		return
	}

	lines := p.Lines
	// Copy from a requisition when no explicit lines were provided.
	if len(lines) == 0 && p.RequisitionID != nil {
		reqLines, _ := h.orm.RequisitionLine.Query().
			Where(entreqline.RequisitionID(*p.RequisitionID), entreqline.ItemTypeEQ(entreqline.ItemTypeInventory)).
			All(r.Context())
		for _, rl := range reqLines {
			lines = append(lines, rfqLinePayload{ItemID: rl.ItemID, Description: rl.Description, Quantity: rl.Quantity})
		}
	}
	for _, l := range lines {
		qty := l.Quantity
		if qty <= 0 {
			qty = 1
		}
		lc := h.orm.RFQLine.Create().SetTenantID(tenantID).SetRfqID(rfq.ID).SetDescription(l.Description).SetQuantity(qty)
		if l.ItemID != nil {
			lc = lc.SetItemID(*l.ItemID)
		}
		if l.UOM != "" {
			lc = lc.SetUom(l.UOM)
		}
		if _, err := lc.Save(r.Context()); err != nil {
			h.log.Warn("create RFQ line failed", zap.Error(err))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "rfq", rfq.ID, "inventory.rfq.created", map[string]any{
		"id": rfq.ID, "rfq_number": rfq.RfqNumber,
	})
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfq.ID)
	writeJSON(w, http.StatusCreated, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// ListRFQs handles GET /inventory/rfqs.
func (h *InventoryExtrasHandler) ListRFQs(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.RFQ.Query().Where(entrfq.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entrfq.StatusEQ(entrfq.Status(s)))
	}
	rows, err := q.Order(ent.Desc(entrfq.FieldCreatedAt)).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list RFQs")
		return
	}
	out := make([]rfqDTO, 0, len(rows))
	for _, rfq := range rows {
		out = append(out, h.rfqToDTO(r.Context(), tenantID, rfq, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetRFQ handles GET /inventory/rfqs/{rfqID}.
func (h *InventoryExtrasHandler) GetRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	full, err := h.loadRFQFull(r.Context(), tenantID, rfqID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// UpdateRFQ handles PUT /inventory/rfqs/{rfqID} — header + (while draft) line replacement.
func (h *InventoryExtrasHandler) UpdateRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	rfq, err := h.orm.RFQ.Query().Where(entrfq.ID(rfqID), entrfq.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}
	var p createRFQPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	upd := h.orm.RFQ.UpdateOneID(rfq.ID).SetTitle(p.Title).SetNotes(p.Notes)
	if p.WarehouseID != nil {
		upd = upd.SetWarehouseID(*p.WarehouseID)
	}
	if p.DueDate != nil {
		upd = upd.SetDueDate(*p.DueDate)
	}
	if _, err := upd.Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update RFQ")
		return
	}
	// Replace lines only while the RFQ is still a draft.
	if p.Lines != nil && rfq.Status == entrfq.StatusDraft {
		_, _ = h.orm.RFQLine.Delete().Where(entrfqline.RfqID(rfq.ID)).Exec(r.Context())
		for _, l := range p.Lines {
			qty := l.Quantity
			if qty <= 0 {
				qty = 1
			}
			lc := h.orm.RFQLine.Create().SetTenantID(tenantID).SetRfqID(rfq.ID).SetDescription(l.Description).SetQuantity(qty)
			if l.ItemID != nil {
				lc = lc.SetItemID(*l.ItemID)
			}
			if l.UOM != "" {
				lc = lc.SetUom(l.UOM)
			}
			if _, err := lc.Save(r.Context()); err != nil {
				h.log.Warn("update RFQ line failed", zap.Error(err))
			}
		}
	}
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfq.ID)
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// DeleteRFQ handles DELETE /inventory/rfqs/{rfqID}.
func (h *InventoryExtrasHandler) DeleteRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	if _, err := h.orm.RFQ.Query().Where(entrfq.ID(rfqID), entrfq.TenantID(tenantID)).Only(r.Context()); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}
	_, _ = h.orm.RFQLine.Delete().Where(entrfqline.RfqID(rfqID)).Exec(r.Context())
	_, _ = h.orm.SupplierResponse.Delete().Where(entsupresp.RfqID(rfqID)).Exec(r.Context())
	_, _ = h.orm.RFQAward.Delete().Where(entrfqaward.RfqID(rfqID)).Exec(r.Context())
	if err := h.orm.RFQ.DeleteOneID(rfqID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete RFQ")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": rfqID})
}

// ─── Suppliers / send ────────────────────────────────────────────────────────

// InviteRFQSuppliers handles POST /inventory/rfqs/{rfqID}/suppliers.
func (h *InventoryExtrasHandler) InviteRFQSuppliers(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	var body struct {
		SupplierIDs []uuid.UUID `json:"supplier_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	invited := 0
	for _, sid := range body.SupplierIDs {
		exists, _ := h.orm.SupplierResponse.Query().
			Where(entsupresp.RfqID(rfqID), entsupresp.SupplierID(sid)).Exist(r.Context())
		if exists {
			continue
		}
		if _, err := h.orm.SupplierResponse.Create().
			SetTenantID(tenantID).SetRfqID(rfqID).SetSupplierID(sid).Save(r.Context()); err != nil {
			h.log.Warn("invite supplier failed", zap.Error(err))
			continue
		}
		invited++
	}
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfqID)
	writeJSON(w, http.StatusOK, map[string]any{"invited": invited, "rfq": h.rfqToDTO(r.Context(), tenantID, full, true)})
}

// RemoveRFQSupplier handles DELETE /inventory/rfqs/{rfqID}/responses/{responseID}.
func (h *InventoryExtrasHandler) RemoveRFQSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	respID, err := uuid.Parse(chi.URLParam(r, "responseID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid response ID")
		return
	}
	if _, err := h.orm.SupplierResponse.Delete().
		Where(entsupresp.ID(respID), entsupresp.RfqID(rfqID), entsupresp.TenantID(tenantID)).
		Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to remove supplier")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

// SendRFQ handles POST /inventory/rfqs/{rfqID}/send.
func (h *InventoryExtrasHandler) SendRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	respCount, _ := h.orm.SupplierResponse.Query().Where(entsupresp.RfqID(rfqID)).Count(r.Context())
	if respCount == 0 {
		writeError(w, http.StatusBadRequest, "NO_SUPPLIERS", "Invite at least one supplier before sending")
		return
	}
	if _, err := h.orm.RFQ.UpdateOneID(rfqID).SetStatus(entrfq.StatusSent).Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "SEND_FAILED", "Failed to send RFQ")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "rfq", rfqID, "inventory.rfq.sent", map[string]any{
		"id": rfqID, "suppliers": respCount,
	})
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfqID)
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// ─── Quotes ──────────────────────────────────────────────────────────────────

// CaptureRFQQuote handles PUT /inventory/rfqs/{rfqID}/responses/{responseID}/quote.
func (h *InventoryExtrasHandler) CaptureRFQQuote(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	respID, err := uuid.Parse(chi.URLParam(r, "responseID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid response ID")
		return
	}
	resp, err := h.orm.SupplierResponse.Query().
		Where(entsupresp.ID(respID), entsupresp.RfqID(rfqID), entsupresp.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier response not found")
		return
	}
	var body struct {
		Currency string          `json:"currency"`
		Notes    string          `json:"notes"`
		Items    []quotedItemDTO `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	items := make([]entschema.QuotedItemJSON, 0, len(body.Items))
	for _, qi := range body.Items {
		items = append(items, entschema.QuotedItemJSON{
			RFQLineID: qi.RFQLineID, UnitPrice: qi.UnitPrice, LeadTimeDays: qi.LeadTimeDays,
			Available: qi.Available, Notes: qi.Notes,
		})
	}
	upd := h.orm.SupplierResponse.UpdateOneID(resp.ID).
		SetQuotedItems(items).
		SetStatus(entsupresp.StatusSubmitted).
		SetSubmittedAt(time.Now())
	if body.Currency != "" {
		upd = upd.SetCurrency(body.Currency)
	}
	if body.Notes != "" {
		upd = upd.SetNotes(body.Notes)
	}
	if _, err := upd.Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "QUOTE_FAILED", "Failed to save quote")
		return
	}
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfqID)
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// DeclineRFQResponse handles POST /inventory/rfqs/{rfqID}/responses/{responseID}/decline.
func (h *InventoryExtrasHandler) DeclineRFQResponse(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	respID, err := uuid.Parse(chi.URLParam(r, "responseID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid response ID")
		return
	}
	if _, err := h.orm.SupplierResponse.Update().
		Where(entsupresp.ID(respID), entsupresp.RfqID(rfqID), entsupresp.TenantID(tenantID)).
		SetStatus(entsupresp.StatusDeclined).Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to decline")
		return
	}
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfqID)
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

// ─── Comparison matrix ───────────────────────────────────────────────────────

type comparisonQuote struct {
	SupplierID   uuid.UUID `json:"supplier_id"`
	SupplierName string    `json:"supplier_name"`
	ResponseID   uuid.UUID `json:"response_id"`
	UnitPrice    float64   `json:"unit_price"`
	LeadTimeDays int       `json:"lead_time_days"`
	Available    bool      `json:"available"`
	LineTotal    float64   `json:"line_total"`
}

type comparisonLine struct {
	RFQLineID      uuid.UUID         `json:"rfq_line_id"`
	Description    string            `json:"description"`
	ItemName       string            `json:"item_name,omitempty"`
	Quantity       float64           `json:"quantity"`
	BestSupplierID *uuid.UUID        `json:"best_supplier_id,omitempty"`
	Quotes         []comparisonQuote `json:"quotes"`
}

type comparisonSupplier struct {
	SupplierID   uuid.UUID `json:"supplier_id"`
	SupplierName string    `json:"supplier_name"`
	ResponseID   uuid.UUID `json:"response_id"`
	Status       string    `json:"status"`
	Currency     string    `json:"currency"`
	GrandTotal   float64   `json:"grand_total"`
}

// RFQComparison handles GET /inventory/rfqs/{rfqID}/comparison — a line × supplier
// quote grid with per-line best (lowest available) and per-supplier grand totals.
func (h *InventoryExtrasHandler) RFQComparison(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	full, err := h.loadRFQFull(r.Context(), tenantID, rfqID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}
	supCache := map[uuid.UUID]string{}
	itemCache := map[uuid.UUID]string{}

	// index quotes: lineID → supplierID → quote
	type qk struct {
		line, supplier uuid.UUID
	}
	quoteByLineSup := map[qk]entschema.QuotedItemJSON{}
	suppliers := make([]comparisonSupplier, 0, len(full.Edges.Responses))
	grand := map[uuid.UUID]float64{}
	for _, resp := range full.Edges.Responses {
		for _, qi := range resp.QuotedItems {
			quoteByLineSup[qk{qi.RFQLineID, resp.SupplierID}] = qi
			grand[resp.SupplierID] += qi.UnitPrice * float64(h.qtyForLine(full, qi.RFQLineID))
		}
		suppliers = append(suppliers, comparisonSupplier{
			SupplierID: resp.SupplierID, ResponseID: resp.ID, Status: string(resp.Status),
			Currency: resp.Currency, SupplierName: h.supplierName(r.Context(), tenantID, resp.SupplierID, supCache),
		})
	}
	for i := range suppliers {
		suppliers[i].GrandTotal = grand[suppliers[i].SupplierID]
	}

	lines := make([]comparisonLine, 0, len(full.Edges.Lines))
	for _, l := range full.Edges.Lines {
		cl := comparisonLine{RFQLineID: l.ID, Description: l.Description, Quantity: l.Quantity}
		if l.ItemID != nil {
			cl.ItemName = h.itemName(r.Context(), tenantID, *l.ItemID, itemCache)
		}
		var bestPrice float64
		hasBest := false
		for _, resp := range full.Edges.Responses {
			qi, found := quoteByLineSup[qk{l.ID, resp.SupplierID}]
			if !found {
				continue
			}
			lineTotal := qi.UnitPrice * float64(l.Quantity)
			cl.Quotes = append(cl.Quotes, comparisonQuote{
				SupplierID: resp.SupplierID, ResponseID: resp.ID,
				SupplierName: h.supplierName(r.Context(), tenantID, resp.SupplierID, supCache),
				UnitPrice:    qi.UnitPrice, LeadTimeDays: qi.LeadTimeDays, Available: qi.Available, LineTotal: lineTotal,
			})
			if qi.Available && (!hasBest || qi.UnitPrice < bestPrice) {
				bestPrice = qi.UnitPrice
				sid := resp.SupplierID
				cl.BestSupplierID = &sid
				hasBest = true
			}
		}
		lines = append(lines, cl)
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "suppliers": suppliers})
}

// ─── Award / convert ─────────────────────────────────────────────────────────

type awardEntry struct {
	RFQLineID  uuid.UUID `json:"rfq_line_id"`
	SupplierID uuid.UUID `json:"supplier_id"`
	UnitPrice  float64   `json:"unit_price"`
	Quantity   float64   `json:"quantity"`
}

// AwardRFQ handles POST /inventory/rfqs/{rfqID}/award. Replaces any prior awards.
func (h *InventoryExtrasHandler) AwardRFQ(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	var body struct {
		Awards []awardEntry `json:"awards"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if len(body.Awards) == 0 {
		writeError(w, http.StatusBadRequest, "NO_AWARDS", "Select at least one line to award")
		return
	}
	// Approval gate: awarding commits the tenant to suppliers. Amount = total award value.
	var awardTotal float64
	for _, a := range body.Awards {
		awardTotal += a.UnitPrice * a.Quantity
	}
	rfqRef := ""
	if rfq, e := h.orm.RFQ.Query().Where(entrfq.ID(rfqID), entrfq.TenantID(tenantID)).Only(r.Context()); e == nil {
		rfqRef = rfq.RfqNumber
	}
	if !h.gateApproval(w, r, tenantID, "rfq", rfqID, rfqRef, awardTotal) {
		return
	}
	// Replace prior awards that have not yet been converted to POs.
	_, _ = h.orm.RFQAward.Delete().Where(entrfqaward.RfqID(rfqID), entrfqaward.PoIDIsNil()).Exec(r.Context())
	for _, a := range body.Awards {
		qty := a.Quantity
		if qty <= 0 {
			qty = h.lineQty(r.Context(), rfqID, a.RFQLineID)
		}
		if _, err := h.orm.RFQAward.Create().
			SetTenantID(tenantID).SetRfqID(rfqID).SetRfqLineID(a.RFQLineID).
			SetSupplierID(a.SupplierID).SetUnitPrice(a.UnitPrice).SetQuantity(qty).
			Save(r.Context()); err != nil {
			h.log.Warn("create award failed", zap.Error(err))
		}
	}
	if _, err := h.orm.RFQ.UpdateOneID(rfqID).SetStatus(entrfq.StatusAwarded).Save(r.Context()); err != nil {
		h.log.Warn("set RFQ awarded failed", zap.Error(err))
	}

	// Auto-create a draft supplier Contract per awarded supplier, linked to this RFQ. Idempotent:
	// skip suppliers that already have a contract for this RFQ (so re-awarding doesn't duplicate).
	awardValueBySupplier := map[uuid.UUID]float64{}
	for _, a := range body.Awards {
		q := a.Quantity
		if q <= 0 {
			q = h.lineQty(r.Context(), rfqID, a.RFQLineID)
		}
		awardValueBySupplier[a.SupplierID] += a.UnitPrice * q
	}
	for sid, val := range awardValueBySupplier {
		exists, _ := h.orm.Contract.Query().
			Where(entcontract.TenantID(tenantID), entcontract.SupplierID(sid), entcontract.RfqID(rfqID)).
			Exist(r.Context())
		if exists {
			continue
		}
		now := time.Now()
		if _, err := h.orm.Contract.Create().
			SetTenantID(tenantID).SetSupplierID(sid).SetRfqID(rfqID).
			SetTitle("Contract from RFQ " + rfqRef).
			SetStartDate(now).SetEndDate(now.AddDate(1, 0, 0)).
			SetValue(val).Save(r.Context()); err != nil {
			h.log.Warn("award RFQ: auto-create contract failed", zap.Error(err))
		}
	}

	h.publishOutbox(r.Context(), tenantID, "rfq", rfqID, "inventory.rfq.awarded", map[string]any{
		"id": rfqID, "awards": len(body.Awards),
	})
	full, _ := h.loadRFQFull(r.Context(), tenantID, rfqID)
	writeJSON(w, http.StatusOK, h.rfqToDTO(r.Context(), tenantID, full, true))
}

func (h *InventoryExtrasHandler) lineQty(ctx context.Context, rfqID, lineID uuid.UUID) float64 {
	l, err := h.orm.RFQLine.Query().Where(entrfqline.ID(lineID), entrfqline.RfqID(rfqID)).Only(ctx)
	if err != nil {
		return 1
	}
	return l.Quantity
}

// ConvertRFQToPOs handles POST /inventory/rfqs/{rfqID}/convert-to-po. Groups
// un-converted awards by supplier and creates one draft PO per supplier.
func (h *InventoryExtrasHandler) ConvertRFQToPOs(w http.ResponseWriter, r *http.Request) {
	tenantID, rfqID, ok := h.rfqParams(w, r)
	if !ok {
		return
	}
	rfq, err := h.orm.RFQ.Query().Where(entrfq.ID(rfqID), entrfq.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "RFQ not found")
		return
	}
	var body struct {
		WarehouseID *uuid.UUID `json:"warehouse_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	warehouseID := body.WarehouseID
	if warehouseID == nil {
		warehouseID = rfq.WarehouseID
	}
	if warehouseID == nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id is required (none set on the RFQ)")
		return
	}

	awards, _ := h.orm.RFQAward.Query().Where(entrfqaward.RfqID(rfqID), entrfqaward.PoIDIsNil()).All(r.Context())
	if len(awards) == 0 {
		writeError(w, http.StatusBadRequest, "NO_AWARDS", "No un-converted awards to order")
		return
	}
	// Group awards by supplier.
	bySupplier := map[uuid.UUID][]*ent.RFQAward{}
	order := []uuid.UUID{}
	for _, a := range awards {
		if _, seen := bySupplier[a.SupplierID]; !seen {
			order = append(order, a.SupplierID)
		}
		bySupplier[a.SupplierID] = append(bySupplier[a.SupplierID], a)
	}

	createdPOs := []map[string]any{}
	for _, sid := range order {
		group := bySupplier[sid]
		poNumber := "PO-" + strings.ToUpper(uuid.New().String()[:8])
		if h.docSvc != nil {
			if n, e := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypePurchaseOrder); e == nil && n != "" {
				poNumber = n
			}
		}
		po, err := h.orm.PurchaseOrder.Create().
			SetTenantID(tenantID).SetSupplierID(sid).SetWarehouseID(*warehouseID).
			SetPoNumber(poNumber).SetRfqID(rfqID).SetNillableProjectID(rfq.ProjectID). // carry the project to the awarded PO
			SetNillableRequisitionID(rfq.RequisitionID).
			SetNotes("From RFQ " + rfq.RfqNumber).Save(r.Context())
		if err != nil {
			h.log.Warn("convert RFQ: create PO failed", zap.Error(err))
			continue
		}
		var total float64
		for _, a := range group {
			line, lerr := h.orm.RFQLine.Get(r.Context(), a.RfqLineID)
			if lerr != nil || line.ItemID == nil {
				continue // PO lines require an item_id; free-text lines are skipped
			}
			lineTotal := a.UnitPrice * float64(a.Quantity)
			total += lineTotal
			if _, err := h.orm.PurchaseOrderLine.Create().
				SetPoID(po.ID).SetItemID(*line.ItemID).SetQuantityOrdered(a.Quantity).
				SetUnitPrice(a.UnitPrice).SetTotalPrice(lineTotal).Save(r.Context()); err != nil {
				h.log.Warn("convert RFQ: create PO line failed", zap.Error(err))
				continue
			}
			_, _ = h.orm.RFQAward.UpdateOneID(a.ID).SetPoID(po.ID).Save(r.Context())
		}
		po, _ = h.orm.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(total).Save(r.Context())
		h.publishOutbox(r.Context(), tenantID, "purchase_order", po.ID, "inventory.purchase_order.created", map[string]any{
			"id": po.ID, "po_number": po.PoNumber, "supplier_id": sid, "total_amount": total, "from_rfq_id": rfqID,
		})
		createdPOs = append(createdPOs, map[string]any{"purchase_order_id": po.ID, "po_number": po.PoNumber, "supplier_id": sid, "total_amount": total})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"purchase_orders": createdPOs, "count": len(createdPOs)})
}

// ─── shared param parsing ────────────────────────────────────────────────────

func (h *InventoryExtrasHandler) rfqParams(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, uuid.Nil, false
	}
	rfqID, err := uuid.Parse(chi.URLParam(r, "rfqID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid RFQ ID")
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, rfqID, true
}
