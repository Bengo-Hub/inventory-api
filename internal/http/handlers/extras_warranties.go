package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authclient "github.com/Bengo-Hub/shared-auth-client"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwarranty "github.com/bengobox/inventory-service/internal/ent/warranty"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// ─── Warranties (retail use case, tier 2+) ────────────────────────────────────
//
// Warranty tracking for serialized items (electronics, hardware, equipment).
// The Warranty ent schema pre-existed with no HTTP surface; this module exposes it
// per the use-case PowerSuite specs (docs/subscription-plans in subscriptions-api).
// Mutations are gated by the `warranties` subscription feature; reads stay open per
// the repo convention so receipts/repair lookups keep working on any plan.

type warrantyDTO struct {
	ID            uuid.UUID  `json:"id"`
	ItemID        uuid.UUID  `json:"item_id"`
	ItemName      string     `json:"item_name"`
	ItemSKU       string     `json:"item_sku"`
	ItemModel     string     `json:"item_model,omitempty"`
	ItemBrand     string     `json:"item_brand,omitempty"`
	SerialNumber  string     `json:"serial_number"`
	CustomerID    *uuid.UUID `json:"customer_id,omitempty"`
	PurchaseDate  time.Time  `json:"purchase_date"`
	WarrantyStart time.Time  `json:"warranty_start"`
	WarrantyEnd   time.Time  `json:"warranty_end"`
	Status        string     `json:"status"`
	Notes         string     `json:"notes,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func warrantyToDTO(wt *ent.Warranty) warrantyDTO {
	dto := warrantyDTO{
		ID:            wt.ID,
		ItemID:        wt.ItemID,
		SerialNumber:  wt.SerialNumber,
		PurchaseDate:  wt.PurchaseDate,
		WarrantyStart: wt.WarrantyStart,
		WarrantyEnd:   wt.WarrantyEnd,
		Status:        wt.Status.String(),
		Notes:         wt.Notes,
		CreatedAt:     wt.CreatedAt,
		UpdatedAt:     wt.UpdatedAt,
	}
	if wt.CustomerID != nil {
		dto.CustomerID = wt.CustomerID
	}
	if wt.Edges.Item != nil {
		dto.ItemName = wt.Edges.Item.Name
		dto.ItemSKU = wt.Edges.Item.Sku
		// Brand/model flow into the warranty: the return desk verifies the exact product a
		// serial belongs to. model is per-item free text; brand comes from the ItemBrand edge
		// when loaded (WithItemBrand on the item query below).
		dto.ItemModel = wt.Edges.Item.Model
		if wt.Edges.Item.Edges.ItemBrand != nil {
			dto.ItemBrand = wt.Edges.Item.Edges.ItemBrand.Name
		}
	}
	// Surface time-based expiry without requiring a background job: an "active" row whose
	// coverage window has lapsed reads as expired (the claim/void writes still persist real
	// status transitions).
	if dto.Status == "active" && time.Now().After(wt.WarrantyEnd) {
		dto.Status = "expired"
	}
	return dto
}

func (h *InventoryExtrasHandler) registerWarrantyRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler) {
	featGate := authclient.RequireFeatureCode("warranties")
	r.Get("/inventory/warranties", h.ListWarranties)
	r.Get("/inventory/warranties/lookup", h.LookupWarrantyBySerial)
	r.Get("/inventory/warranties/{warrantyID}", h.GetWarranty)
	r.With(featGate, perm(rbac.PermItemsAdd)).Post("/inventory/warranties", h.CreateWarranty)
	r.With(featGate, perm(rbac.PermItemsChange)).Put("/inventory/warranties/{warrantyID}", h.UpdateWarranty)
	r.With(featGate, perm(rbac.PermItemsChange)).Post("/inventory/warranties/{warrantyID}/claim", h.ClaimWarranty)
	r.With(featGate, perm(rbac.PermItemsChange)).Post("/inventory/warranties/{warrantyID}/void", h.VoidWarranty)
	r.With(featGate, perm(rbac.PermItemsDelete)).Delete("/inventory/warranties/{warrantyID}", h.DeleteWarranty)
}

// ListWarranties handles GET /inventory/warranties?status=&item_id=&search=.
func (h *InventoryExtrasHandler) ListWarranties(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	q := h.orm.Warranty.Query().
		Where(entwarranty.TenantID(tenantID)).
		WithItem(func(iq *ent.ItemQuery) { iq.WithItemBrand() }).
		Order(ent.Desc(entwarranty.FieldCreatedAt))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entwarranty.StatusEQ(entwarranty.Status(s)))
	}
	if itemID, perr := uuid.Parse(r.URL.Query().Get("item_id")); perr == nil {
		q = q.Where(entwarranty.ItemID(itemID))
	}
	rows, err := q.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list warranties")
		return
	}
	search := strings.ToLower(r.URL.Query().Get("search"))
	result := make([]warrantyDTO, 0, len(rows))
	for _, wt := range rows {
		dto := warrantyToDTO(wt)
		if search != "" &&
			!strings.Contains(strings.ToLower(dto.SerialNumber), search) &&
			!strings.Contains(strings.ToLower(dto.ItemName), search) &&
			!strings.Contains(strings.ToLower(dto.ItemSKU), search) {
			continue
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, result)
}

// LookupWarrantyBySerial handles GET /inventory/warranties/lookup?serial= — the POS/repair
// desk fast path: find coverage for a serial number regardless of item.
func (h *InventoryExtrasHandler) LookupWarrantyBySerial(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	serial := strings.TrimSpace(r.URL.Query().Get("serial"))
	if serial == "" {
		writeError(w, http.StatusBadRequest, "INVALID_SERIAL", "serial query parameter is required")
		return
	}
	rows, err := h.orm.Warranty.Query().
		Where(entwarranty.TenantID(tenantID), entwarranty.SerialNumberEqualFold(serial)).
		WithItem(func(iq *ent.ItemQuery) { iq.WithItemBrand() }).
		Order(ent.Desc(entwarranty.FieldWarrantyEnd)).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to look up warranty")
		return
	}
	result := make([]warrantyDTO, 0, len(rows))
	for _, wt := range rows {
		result = append(result, warrantyToDTO(wt))
	}
	writeJSON(w, http.StatusOK, result)
}

// GetWarranty handles GET /inventory/warranties/{warrantyID}.
func (h *InventoryExtrasHandler) GetWarranty(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "warrantyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warranty ID")
		return
	}
	wt, err := h.orm.Warranty.Query().
		Where(entwarranty.ID(id), entwarranty.TenantID(tenantID)).
		WithItem(func(iq *ent.ItemQuery) { iq.WithItemBrand() }).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Warranty not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load warranty")
		return
	}
	writeJSON(w, http.StatusOK, warrantyToDTO(wt))
}

type warrantyWriteRequest struct {
	ItemID        uuid.UUID  `json:"item_id"`
	SerialNumber  string     `json:"serial_number"`
	CustomerID    *uuid.UUID `json:"customer_id"`
	PurchaseDate  *time.Time `json:"purchase_date"`
	WarrantyStart *time.Time `json:"warranty_start"`
	WarrantyEnd   *time.Time `json:"warranty_end"`
	// Convenience: months of coverage from warranty_start when warranty_end is omitted.
	CoverageMonths int    `json:"coverage_months"`
	Notes          string `json:"notes"`
}

// CreateWarranty handles POST /inventory/warranties.
func (h *InventoryExtrasHandler) CreateWarranty(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req warrantyWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid JSON body")
		return
	}
	req.SerialNumber = strings.TrimSpace(req.SerialNumber)
	if req.ItemID == uuid.Nil || req.SerialNumber == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "item_id and serial_number are required")
		return
	}
	// The item must belong to this tenant.
	ok, err := h.orm.Item.Query().Where(entitem.ID(req.ItemID), entitem.TenantID(tenantID)).Exist(r.Context())
	if err != nil || !ok {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM", "Item not found for this tenant")
		return
	}
	now := time.Now()
	purchase := now
	if req.PurchaseDate != nil {
		purchase = *req.PurchaseDate
	}
	start := purchase
	if req.WarrantyStart != nil {
		start = *req.WarrantyStart
	}
	var end time.Time
	switch {
	case req.WarrantyEnd != nil:
		end = *req.WarrantyEnd
	case req.CoverageMonths > 0:
		end = start.AddDate(0, req.CoverageMonths, 0)
	default:
		end = start.AddDate(1, 0, 0) // sensible default: 12 months
	}
	if !end.After(start) {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "warranty_end must be after warranty_start")
		return
	}
	b := h.orm.Warranty.Create().
		SetTenantID(tenantID).
		SetItemID(req.ItemID).
		SetSerialNumber(req.SerialNumber).
		SetPurchaseDate(purchase).
		SetWarrantyStart(start).
		SetWarrantyEnd(end).
		SetNotes(req.Notes)
	if req.CustomerID != nil {
		b.SetCustomerID(*req.CustomerID)
	}
	wt, err := b.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to create warranty")
		return
	}
	writeJSON(w, http.StatusCreated, warrantyToDTO(wt))
}

// UpdateWarranty handles PUT /inventory/warranties/{warrantyID}.
func (h *InventoryExtrasHandler) UpdateWarranty(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "warrantyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warranty ID")
		return
	}
	var req warrantyWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid JSON body")
		return
	}
	wt, err := h.orm.Warranty.Query().
		Where(entwarranty.ID(id), entwarranty.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Warranty not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load warranty")
		return
	}
	upd := wt.Update()
	if s := strings.TrimSpace(req.SerialNumber); s != "" {
		upd.SetSerialNumber(s)
	}
	if req.CustomerID != nil {
		upd.SetCustomerID(*req.CustomerID)
	}
	if req.PurchaseDate != nil {
		upd.SetPurchaseDate(*req.PurchaseDate)
	}
	if req.WarrantyStart != nil {
		upd.SetWarrantyStart(*req.WarrantyStart)
	}
	if req.WarrantyEnd != nil {
		upd.SetWarrantyEnd(*req.WarrantyEnd)
	}
	if req.Notes != "" {
		upd.SetNotes(req.Notes)
	}
	saved, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update warranty")
		return
	}
	saved.Edges.Item = wt.Edges.Item
	writeJSON(w, http.StatusOK, warrantyToDTO(saved))
}

// setWarrantyStatus is the shared claim/void transition.
func (h *InventoryExtrasHandler) setWarrantyStatus(w http.ResponseWriter, r *http.Request, status entwarranty.Status) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "warrantyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warranty ID")
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	wt, err := h.orm.Warranty.Query().
		Where(entwarranty.ID(id), entwarranty.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Warranty not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load warranty")
		return
	}
	if wt.Status == entwarranty.StatusVoided {
		writeError(w, http.StatusConflict, "INVALID_STATE", "Warranty is voided")
		return
	}
	if status == entwarranty.StatusClaimed && time.Now().After(wt.WarrantyEnd) {
		writeError(w, http.StatusConflict, "EXPIRED", "Warranty coverage has ended")
		return
	}
	upd := wt.Update().SetStatus(status)
	if body.Notes != "" {
		upd.SetNotes(strings.TrimSpace(wt.Notes + "\n" + body.Notes))
	}
	saved, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update warranty status")
		return
	}
	writeJSON(w, http.StatusOK, warrantyToDTO(saved))
}

// ClaimWarranty handles POST /inventory/warranties/{warrantyID}/claim.
func (h *InventoryExtrasHandler) ClaimWarranty(w http.ResponseWriter, r *http.Request) {
	h.setWarrantyStatus(w, r, entwarranty.StatusClaimed)
}

// VoidWarranty handles POST /inventory/warranties/{warrantyID}/void.
func (h *InventoryExtrasHandler) VoidWarranty(w http.ResponseWriter, r *http.Request) {
	h.setWarrantyStatus(w, r, entwarranty.StatusVoided)
}

// DeleteWarranty handles DELETE /inventory/warranties/{warrantyID}.
func (h *InventoryExtrasHandler) DeleteWarranty(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "warrantyID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid warranty ID")
		return
	}
	n, err := h.orm.Warranty.Delete().
		Where(entwarranty.ID(id), entwarranty.TenantID(tenantID)).
		Exec(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to delete warranty")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Warranty not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
