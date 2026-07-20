package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Bulk item actions (DataTable multi-select). Both endpoints are idempotent:
// re-posting the same ids is a no-op reported via `skipped`, never an error.

type bulkItemActionRequest struct {
	// Item ids (uuids). The DataTable selects by row id.
	IDs []string `json:"ids"`
	// bulk-status only: one of activate | deactivate | not_for_sale_on | not_for_sale_off.
	Action string `json:"action,omitempty"`
}

// parseBulkIDs validates and de-duplicates the request ids (cap 500 per call —
// a full DataTable page is ≤100 rows, so anything larger is a runaway client).
func parseBulkIDs(raw []string) ([]uuid.UUID, string) {
	if len(raw) == 0 {
		return nil, "ids is required"
	}
	if len(raw) > 500 {
		return nil, "too many ids (max 500 per request)"
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, "invalid id: " + s
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, ""
}

// BulkDeleteItems handles POST /v1/{tenant}/inventory/items/bulk-delete —
// soft-deletes many items (same semantics as DELETE /items/{sku}: is_active=false).
//
//	@Summary      Bulk delete items
//	@Description  Soft-deletes the given items (is_active=false). Idempotent — already-inactive or unknown ids are reported in skipped.
//	@Tags         items
//	@Accept       json
//	@Produce      json
//	@Param        tenant  path      string                 true  "Tenant ID"
//	@Param        body    body      bulkItemActionRequest  true  "Item ids"
//	@Success      200     {object}  items.BulkActionResult
//	@Failure      400     {object}  map[string]string
//	@Router       /{tenant}/inventory/items/bulk-delete [post]
func (h *InventoryHandler) BulkDeleteItems(w http.ResponseWriter, r *http.Request) {
	h.bulkItemAction(w, r, items.BulkActionDelete)
}

// BulkItemStatus handles POST /v1/{tenant}/inventory/items/bulk-status —
// applies one status action (activate / deactivate / not_for_sale_on / not_for_sale_off).
//
//	@Summary      Bulk change item status
//	@Description  Applies one status action to many items: activate, deactivate, not_for_sale_on, not_for_sale_off. Idempotent — items already in the target state are reported in skipped.
//	@Tags         items
//	@Accept       json
//	@Produce      json
//	@Param        tenant  path      string                 true  "Tenant ID"
//	@Param        body    body      bulkItemActionRequest  true  "Item ids + action"
//	@Success      200     {object}  items.BulkActionResult
//	@Failure      400     {object}  map[string]string
//	@Router       /{tenant}/inventory/items/bulk-status [post]
func (h *InventoryHandler) BulkItemStatus(w http.ResponseWriter, r *http.Request) {
	h.bulkItemAction(w, r, "")
}

// bulkItemAction is the shared request pipeline; fixedAction pins bulk-delete,
// otherwise the action comes from the body (bulk-status).
func (h *InventoryHandler) bulkItemAction(w http.ResponseWriter, r *http.Request, fixedAction string) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req bulkItemActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid JSON body")
		return
	}
	action := fixedAction
	if action == "" {
		action = req.Action
		if !items.ValidBulkAction(action) || action == items.BulkActionDelete {
			// delete has its own endpoint (and its own RBAC gate) — reject here.
			writeError(w, http.StatusBadRequest, "INVALID_ACTION", "action must be one of: activate, deactivate, not_for_sale_on, not_for_sale_off")
			return
		}
	}
	ids, msg := parseBulkIDs(req.IDs)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "INVALID_IDS", msg)
		return
	}
	res, err := h.itemsSvc.BulkItemAction(r.Context(), tenantID, ids, action)
	if err != nil {
		h.log.Error("bulk item action failed", zap.String("action", action), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "BULK_ACTION_FAILED", "Bulk action failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}
