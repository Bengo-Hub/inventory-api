package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	entstockadjustment "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
)

// ─── Activity ─────────────────────────────────────────────────────────────────

type activityItemDTO struct {
	ID          uuid.UUID `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
	Delta       *int      `json:"delta,omitempty"`
}

func (h *InventoryExtrasHandler) ListActivity(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	adjustments, err := h.orm.StockAdjustment.Query().
		Where(entstockadjustment.TenantID(tenantID)).
		Order(entstockadjustment.ByAdjustedAt()).
		Limit(10).
		All(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, []activityItemDTO{})
		return
	}

	result := make([]activityItemDTO, 0, len(adjustments))
	for _, adj := range adjustments {
		desc := adj.Reason.String() + " stock adjustment"
		if adj.Notes != "" {
			desc = adj.Notes
		}
		delta := int(adj.QuantityChange)
		result = append(result, activityItemDTO{
			ID:          adj.ID,
			Type:        "adjustment",
			Description: desc,
			Timestamp:   adj.AdjustedAt,
			Delta:       &delta,
		})
	}
	writeJSON(w, http.StatusOK, result)
}
