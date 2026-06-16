package handlers

import (
	"net/http"
	"time"

	"github.com/Bengo-Hub/pagination"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
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

	p := pagination.Parse(r)
	q := h.orm.StockAdjustment.Query().Where(entstockadjustment.TenantID(tenantID))
	total, _ := q.Clone().Count(r.Context())

	// Most-recent first (this is the "recent activity" feed).
	adjustments, err := q.
		Order(ent.Desc(entstockadjustment.FieldAdjustedAt)).
		Limit(p.Limit).
		Offset(p.Offset).
		All(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, pagination.NewResponse([]activityItemDTO{}, 0, p))
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
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}
