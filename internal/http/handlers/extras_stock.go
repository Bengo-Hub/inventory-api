package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// ─── Stock ────────────────────────────────────────────────────────────────────

type stockLevelDTO struct {
	ID            uuid.UUID `json:"id"`
	ItemName      string    `json:"item_name"`
	SKU           string    `json:"sku"`
	WarehouseName string    `json:"warehouse_name"`
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	Available     int       `json:"available"`
	Reserved      int       `json:"reserved"`
	ReorderPoint  *int      `json:"reorder_point"`
	Unit          string    `json:"unit"`
}

func (h *InventoryExtrasHandler) ListStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	search := r.URL.Query().Get("search")

	balances, err := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
		WithItem().
		WithWarehouse().
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list stock")
		return
	}

	result := make([]stockLevelDTO, 0, len(balances))
	for _, b := range balances {
		itemName, sku := "", ""
		if b.Edges.Item != nil {
			itemName = b.Edges.Item.Name
			sku = b.Edges.Item.Sku
		}
		warehouseName := ""
		if b.Edges.Warehouse != nil {
			warehouseName = b.Edges.Warehouse.Name
		}
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(itemName), needle) &&
				!strings.Contains(strings.ToLower(sku), needle) &&
				!strings.Contains(strings.ToLower(warehouseName), needle) {
				continue
			}
		}
		var reorderPoint *int
		if b.ReorderLevel > 0 {
			v := b.ReorderLevel
			reorderPoint = &v
		}
		result = append(result, stockLevelDTO{
			ID:            b.ID,
			ItemName:      itemName,
			SKU:           sku,
			WarehouseName: warehouseName,
			WarehouseID:   b.WarehouseID,
			Available:     b.Available,
			Reserved:      b.Reserved,
			ReorderPoint:  reorderPoint,
			Unit:          b.UnitOfMeasure,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// ─── Reorder Config ───────────────────────────────────────────────────────────

type reorderConfigInput struct {
	WarehouseID         uuid.UUID  `json:"warehouse_id"`
	ReorderLevel        int        `json:"reorder_level"`
	ReorderQuantity     int        `json:"reorder_quantity"`
	PreferredSupplierID *uuid.UUID `json:"preferred_supplier_id"`
	AutoReorderEnabled  bool       `json:"auto_reorder_enabled"`
}

// UpdateReorderConfig handles PUT /inventory/stock/{sku}/reorder-config.
func (h *InventoryExtrasHandler) UpdateReorderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	sku := chi.URLParam(r, "sku")

	var req reorderConfigInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	// Find item by SKU
	item, err := h.orm.Item.Query().
		Where(entitem.Sku(sku), entitem.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}

	// Find or create balance record for this item+warehouse
	q := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID), entinventorybalance.ItemID(item.ID))
	if req.WarehouseID != uuid.Nil {
		q = q.Where(entinventorybalance.WarehouseID(req.WarehouseID))
	}

	bal, balErr := q.First(r.Context())
	if balErr != nil {
		if !ent.IsNotFound(balErr) {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to query balance")
			return
		}
		if req.WarehouseID == uuid.Nil {
			writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id required when creating reorder config")
			return
		}
		createQ := h.orm.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(item.ID).
			SetWarehouseID(req.WarehouseID).
			SetReorderLevel(req.ReorderLevel).
			SetReorderQuantity(req.ReorderQuantity).
			SetAutoReorderEnabled(req.AutoReorderEnabled)
		if req.PreferredSupplierID != nil {
			createQ = createQ.SetPreferredSupplierID(*req.PreferredSupplierID)
		}
		_, err = createQ.Save(r.Context())
	} else {
		updateQ := h.orm.InventoryBalance.UpdateOneID(bal.ID).
			SetReorderLevel(req.ReorderLevel).
			SetReorderQuantity(req.ReorderQuantity).
			SetAutoReorderEnabled(req.AutoReorderEnabled)
		if req.PreferredSupplierID != nil {
			updateQ = updateQ.SetPreferredSupplierID(*req.PreferredSupplierID)
		}
		_, err = updateQ.Save(r.Context())
	}
	if err != nil {
		h.log.Error("update reorder config failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update reorder config")
		return
	}

	// Unused import guard
	_ = entwarehouse.TenantID
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
