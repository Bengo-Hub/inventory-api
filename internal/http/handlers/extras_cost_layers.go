package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	entib "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
)

// BackfillCostLayers handles POST /inventory/cost-layers/backfill — a one-shot, idempotent,
// per-tenant admin action that seeds an opening cost layer for every (item, warehouse) balance
// that has on-hand stock but no active cost layer yet: the gap between this feature shipping and
// however far back the tenant's existing stock was actually bought. Without this, StockValuation
// and COGS would read zero (or silently fall back to the item's standard cost) for every unit
// already on hand on cutover day. Safe to re-run any time: an (item, warehouse) that already has
// an active layer — from this backfill or a real goods receipt — is skipped, never duplicated.
func (h *InventoryExtrasHandler) BackfillCostLayers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()

	balances, err := h.orm.InventoryBalance.Query().
		Where(entib.TenantID(tenantID), entib.OnHandGT(0)).
		All(ctx)
	if err != nil {
		h.log.Error("backfill cost layers: query balances failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Failed to load stock balances")
		return
	}

	// Standard cost per item — the best estimate available for stock that predates any real
	// purchase record, since no receipt/lot data exists for it yet.
	itemIDs := make([]uuid.UUID, 0, len(balances))
	seen := make(map[uuid.UUID]bool, len(balances))
	for _, b := range balances {
		if !seen[b.ItemID] {
			seen[b.ItemID] = true
			itemIDs = append(itemIDs, b.ItemID)
		}
	}
	costByItem := make(map[uuid.UUID]*float64, len(itemIDs))
	if len(itemIDs) > 0 {
		items, ierr := h.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).
			Select(entitem.FieldID, entitem.FieldCostPrice).
			All(ctx)
		if ierr == nil {
			for _, it := range items {
				costByItem[it.ID] = it.CostPrice
			}
		}
	}

	seeded, skipped := 0, 0
	now := time.Now()
	for _, b := range balances {
		exists, eerr := h.orm.InventoryLot.Query().
			Where(entlot.TenantID(tenantID), entlot.ItemID(b.ItemID), entlot.WarehouseID(b.WarehouseID), entlot.StatusEQ(entlot.StatusActive)).
			Exist(ctx)
		if eerr != nil || exists {
			skipped++
			continue
		}
		// Deterministic per (item, warehouse) so a re-run of this endpoint hits the same
		// (tenant_id, item_id, lot_number) row instead of creating a duplicate — the lot-number
		// unique index isn't warehouse-scoped, so the warehouse ID must be baked into the string
		// itself to avoid colliding when the same item has opening stock in two warehouses.
		lotNo := "OPENING-" + b.ItemID.String()[:8] + "-" + b.WarehouseID.String()[:8]
		create := h.orm.InventoryLot.Create().
			SetTenantID(tenantID).
			SetItemID(b.ItemID).
			SetWarehouseID(b.WarehouseID).
			SetLotNumber(lotNo).
			SetQuantity(b.OnHand).
			SetStatus("active").
			SetIsCostLayer(true).
			SetReceivedAt(now).
			SetSupplierReference("opening-balance-backfill")
		if cost := costByItem[b.ItemID]; cost != nil && *cost > 0 {
			create = create.SetCostPrice(*cost)
		}
		if _, cerr := create.Save(ctx); cerr != nil {
			h.log.Warn("backfill cost layer failed",
				zap.String("item_id", b.ItemID.String()),
				zap.String("warehouse_id", b.WarehouseID.String()),
				zap.Error(cerr))
			continue
		}
		seeded++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"seeded":         seeded,
		"skipped":        skipped,
		"total_balances": len(balances),
	})
}
