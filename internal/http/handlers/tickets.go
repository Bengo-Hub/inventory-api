package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/tickets"
)

// GetEventAvailability handles GET /inventory/events/{id}/availability.
func (h *InventoryHandler) GetEventAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_EVENT", "Invalid event id")
		return
	}
	av, err := h.ticketsSvc.EventAvailability(r.Context(), tenantID, eventID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	writeJSON(w, http.StatusOK, av)
}

// CreateTicket handles POST /inventory/tickets — issues a ticket with oversell enforcement.
func (h *InventoryHandler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var in tickets.IssueInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if in.EventItemID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_EVENT", "event_item_id is required")
		return
	}
	t, err := h.ticketsSvc.IssueTicket(r.Context(), tenantID, in)
	if err != nil {
		h.log.Warn("issue ticket failed", zap.Error(err))
		writeError(w, http.StatusConflict, "ISSUE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// ListTickets handles GET /inventory/tickets (filter: event_item_id, status, buyer_id).
func (h *InventoryHandler) ListTickets(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var eventPtr, buyerPtr *uuid.UUID
	if v := r.URL.Query().Get("event_item_id"); v != "" {
		if id, e := uuid.Parse(v); e == nil {
			eventPtr = &id
		}
	}
	if v := r.URL.Query().Get("buyer_id"); v != "" {
		if id, e := uuid.Parse(v); e == nil {
			buyerPtr = &id
		}
	}
	status := r.URL.Query().Get("status")
	p := pagination.Parse(r)
	rows, total, err := h.ticketsSvc.List(r.Context(), tenantID, eventPtr, status, buyerPtr, p.Limit, p.Offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list tickets")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(rows, total, p))
}

// GetTicket handles GET /inventory/tickets/{code}.
func (h *InventoryHandler) GetTicket(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	code := chi.URLParam(r, "code")
	t, err := h.ticketsSvc.GetByCode(r.Context(), tenantID, code)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Ticket not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// RedeemTicket handles POST /inventory/tickets/{code}/redeem (check-in).
func (h *InventoryHandler) RedeemTicket(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	code := chi.URLParam(r, "code")
	var in tickets.RedeemInput
	_ = json.NewDecoder(r.Body).Decode(&in) // body optional
	t, err := h.ticketsSvc.RedeemTicket(r.Context(), tenantID, code, in)
	if err != nil {
		writeError(w, http.StatusConflict, "REDEEM_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// CancelTicket handles POST /inventory/tickets/{id}/cancel (releases capacity).
func (h *InventoryHandler) CancelTicket(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid ticket id")
		return
	}
	t, err := h.ticketsSvc.CancelTicket(r.Context(), tenantID, ticketID)
	if err != nil {
		writeError(w, http.StatusConflict, "CANCEL_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}
