package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/modifiers"
)

// ListModifierGroups handles GET /inventory/items/{itemId}/modifier-groups
func (h *InventoryHandler) ListModifierGroups(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier groups not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	itemID, err := uuid.Parse(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM_ID", "Invalid item ID")
		return
	}

	groups, err := h.modifiersSvc.ListModifierGroups(r.Context(), tenantID, itemID)
	if err != nil {
		h.log.Error("list modifier groups failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list modifier groups")
		return
	}

	writeJSON(w, http.StatusOK, groups)
}

// CreateModifierGroup handles POST /inventory/modifier-groups
func (h *InventoryHandler) CreateModifierGroup(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier groups not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req modifiers.CreateModifierGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION", "Name is required")
		return
	}
	if req.ItemID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "VALIDATION", "item_id is required")
		return
	}

	group, err := h.modifiersSvc.CreateModifierGroup(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create modifier group failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to create modifier group")
		return
	}

	writeJSON(w, http.StatusCreated, group)
}

// UpdateModifierGroup handles PUT /inventory/modifier-groups/{id}
func (h *InventoryHandler) UpdateModifierGroup(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier groups not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	groupID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid modifier group ID")
		return
	}

	var req modifiers.UpdateModifierGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	group, err := h.modifiersSvc.UpdateModifierGroup(r.Context(), tenantID, groupID, req)
	if err != nil {
		h.log.Error("update modifier group failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update modifier group")
		return
	}

	writeJSON(w, http.StatusOK, group)
}

// DeleteModifierGroup handles DELETE /inventory/modifier-groups/{id}
func (h *InventoryHandler) DeleteModifierGroup(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier groups not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	groupID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid modifier group ID")
		return
	}

	if err := h.modifiersSvc.DeleteModifierGroup(r.Context(), tenantID, groupID); err != nil {
		h.log.Error("delete modifier group failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to delete modifier group")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateModifierOption handles POST /inventory/modifier-groups/{id}/options
func (h *InventoryHandler) CreateModifierOption(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier options not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	groupID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid modifier group ID")
		return
	}

	var req modifiers.CreateModifierOptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION", "Name is required")
		return
	}

	option, err := h.modifiersSvc.CreateModifierOption(r.Context(), tenantID, groupID, req)
	if err != nil {
		h.log.Error("create modifier option failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to create modifier option")
		return
	}

	writeJSON(w, http.StatusCreated, option)
}

// UpdateModifierOption handles PUT /inventory/modifier-options/{id}
func (h *InventoryHandler) UpdateModifierOption(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier options not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	optionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid modifier option ID")
		return
	}

	var req modifiers.UpdateModifierOptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	option, err := h.modifiersSvc.UpdateModifierOption(r.Context(), tenantID, optionID, req)
	if err != nil {
		h.log.Error("update modifier option failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update modifier option")
		return
	}

	writeJSON(w, http.StatusOK, option)
}

// DeleteModifierOption handles DELETE /inventory/modifier-options/{id}
func (h *InventoryHandler) DeleteModifierOption(w http.ResponseWriter, r *http.Request) {
	if h.modifiersSvc == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Modifier options not enabled")
		return
	}

	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	optionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid modifier option ID")
		return
	}

	if err := h.modifiersSvc.DeleteModifierOption(r.Context(), tenantID, optionID); err != nil {
		h.log.Error("delete modifier option failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to delete modifier option")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
