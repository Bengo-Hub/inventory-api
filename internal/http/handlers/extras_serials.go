package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entserial "github.com/bengobox/inventory-service/internal/ent/inventoryserial"
)

// ─── Per-unit serial registry (read) ────────────────────────────────────────
// Serial rows are written at goods-receipt post and transitioned to "sold" by the
// pos.sale.finalized consumer. These endpoints expose them for the Serials UI.

func (h *InventoryExtrasHandler) registerSerialRoutes(r chi.Router) {
	r.Get("/inventory/serials", h.ListSerials)
	r.Get("/inventory/items/{itemID}/serials", h.ListItemSerials)
}

type serialDTO struct {
	ID           uuid.UUID  `json:"id"`
	ItemID       uuid.UUID  `json:"item_id"`
	WarehouseID  *uuid.UUID `json:"warehouse_id,omitempty"`
	SerialNumber string     `json:"serial_number"`
	Status       string     `json:"status"`
	ReceivedAt   string     `json:"received_at,omitempty"`
	SoldAt       *string    `json:"sold_at,omitempty"`
}

func serialToDTO(s *ent.InventorySerial) serialDTO {
	dto := serialDTO{
		ID: s.ID, ItemID: s.ItemID, WarehouseID: s.WarehouseID,
		SerialNumber: s.SerialNumber, Status: string(s.Status),
		ReceivedAt: s.ReceivedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if s.SoldAt != nil {
		v := s.SoldAt.Format("2006-01-02T15:04:05Z07:00")
		dto.SoldAt = &v
	}
	return dto
}

// ListSerials handles GET /inventory/serials?item_id=&status=&q=
func (h *InventoryExtrasHandler) ListSerials(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.InventorySerial.Query().Where(entserial.TenantID(tenantID))
	if v := r.URL.Query().Get("item_id"); v != "" {
		if id, e := uuid.Parse(v); e == nil {
			q = q.Where(entserial.ItemID(id))
		}
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q = q.Where(entserial.StatusEQ(entserial.Status(v)))
	}
	if v := r.URL.Query().Get("q"); v != "" {
		q = q.Where(entserial.SerialNumberContainsFold(v))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entserial.FieldReceivedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list serials")
		return
	}
	out := make([]serialDTO, len(rows))
	for i, s := range rows {
		out[i] = serialToDTO(s)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(out, total, p))
}

// ListItemSerials handles GET /inventory/items/{itemID}/serials
func (h *InventoryExtrasHandler) ListItemSerials(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM", "Invalid item ID")
		return
	}
	q := h.orm.InventorySerial.Query().Where(entserial.TenantID(tenantID), entserial.ItemID(itemID))
	if v := r.URL.Query().Get("status"); v != "" {
		q = q.Where(entserial.StatusEQ(entserial.Status(v)))
	}
	rows, err := q.Order(ent.Desc(entserial.FieldReceivedAt)).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list serials")
		return
	}
	out := make([]serialDTO, len(rows))
	for i, s := range rows {
		out[i] = serialToDTO(s)
	}
	writeJSON(w, http.StatusOK, out)
}
