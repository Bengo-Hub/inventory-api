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
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
)

// ─── Stock ────────────────────────────────────────────────────────────────────

type stockLevelDTO struct {
	ID            uuid.UUID  `json:"id"`
	ItemName      string     `json:"item_name"`
	SKU           string     `json:"sku"`
	WarehouseName string     `json:"warehouse_name"`
	WarehouseID   uuid.UUID  `json:"warehouse_id"`
	Available     float64    `json:"available"`
	Reserved      float64    `json:"reserved"`
	ReorderPoint  *int       `json:"reorder_point"`
	Unit          string     `json:"unit"`
	UnitID        *uuid.UUID `json:"unit_id"`
	CategoryID    *uuid.UUID `json:"category_id"`
	CategoryName  string     `json:"category_name"`
	Type          string     `json:"type"`
}

// stockableTypes are the item types that hold physical on-hand stock. SERVICE,
// VOUCHER and RECIPE never appear on the stock levels list (mirrors
// items.isStockTracked which governs whether a balance is created at all).
var stockableTypes = []entitem.Type{entitem.TypeGOODS, entitem.TypeINGREDIENT, entitem.TypeEQUIPMENT}

func (h *InventoryExtrasHandler) ListStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	q := r.URL.Query()
	search := q.Get("search")
	lowStock := q.Get("low_stock") == "true"
	outOfStock := q.Get("out_of_stock") == "true"
	categoryIDStr := q.Get("category_id")
	typeFilter := strings.ToUpper(strings.TrimSpace(q.Get("type")))
	warehouseIDStr := q.Get("warehouse_id")

	// Always constrain to stockable item types so non-stock catalog items
	// (services, vouchers, recipes) never surface on the stock levels list.
	itemPreds := []predicate.Item{entitem.TypeIn(stockableTypes...)}
	if categoryIDStr != "" {
		if cid, e := uuid.Parse(categoryIDStr); e == nil {
			itemPreds = append(itemPreds, entitem.CategoryID(cid))
		}
	}
	if typeFilter != "" {
		// Narrow to the requested type (still bounded by stockableTypes above).
		itemPreds = append(itemPreds, entitem.TypeEQ(entitem.Type(typeFilter)))
	}

	balQuery := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
		Where(entinventorybalance.HasItemWith(itemPreds...)).
		WithItem(func(iq *ent.ItemQuery) { iq.WithItemCategory() }).
		WithWarehouse()
	if warehouseIDStr != "" {
		if wid, e := uuid.Parse(warehouseIDStr); e == nil {
			balQuery = balQuery.Where(entinventorybalance.WarehouseID(wid))
		}
	} else if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		// Outlet drill-down (X-Outlet-ID) with no explicit warehouse filter: scope the stock
		// list to the selected outlet's warehouses (+ shared/HQ warehouses with no outlet link),
		// mirroring the ListItems outlet separation — selecting a branch anywhere in the app
		// must show that branch's stock, not the whole tenant's.
		if outletID, e := uuid.Parse(outletStr); e == nil {
			balQuery = balQuery.Where(entinventorybalance.HasWarehouseWith(
				entwarehouse.Or(
					entwarehouse.OutletIDEQ(outletID),
					entwarehouse.OutletIDIsNil(),
				),
			))
		}
	}

	balances, err := balQuery.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list stock")
		return
	}

	result := make([]stockLevelDTO, 0, len(balances))
	for _, b := range balances {
		itemName, sku, typeStr, categoryName := "", "", "", ""
		var categoryID, unitID *uuid.UUID
		if it := b.Edges.Item; it != nil {
			itemName = it.Name
			sku = it.Sku
			typeStr = string(it.Type)
			categoryID = it.CategoryID
			unitID = it.UnitID
			if it.Edges.ItemCategory != nil {
				categoryName = it.Edges.ItemCategory.Name
			}
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

		// Status filters: out-of-stock = nothing available; low = at/below reorder but > 0.
		isOut := b.Available <= 0
		isLow := reorderPoint != nil && b.Available > 0 && b.Available <= float64(*reorderPoint)
		if outOfStock && !isOut {
			continue
		}
		if lowStock && !isLow {
			continue
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
			UnitID:        unitID,
			CategoryID:    categoryID,
			CategoryName:  categoryName,
			Type:          typeStr,
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
