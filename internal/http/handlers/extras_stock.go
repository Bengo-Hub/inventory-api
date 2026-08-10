package handlers

import (
	"context"
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
	LocationID    *uuid.UUID `json:"location_id,omitempty"`
	LocationName  string     `json:"location_name,omitempty"`
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

// stockLevelFilters are the shared query filters for the stock-levels list — used by both
// the JSON ListStock endpoint and the branded PDF/CSV export (StockExportPDF), so the two
// surfaces can never drift on what counts as "in scope".
type stockLevelFilters struct {
	Search      string
	LowStock    bool
	OutOfStock  bool
	CategoryID  *uuid.UUID
	TypeFilter  string
	WarehouseID *uuid.UUID
	LocationID  *uuid.UUID
	// ItemID scopes to a single item's balances across every warehouse — used by the item
	// drawer's "Locations" panel and the stock-move dialog's "available at source" lookup.
	ItemID *uuid.UUID
	// OutletID scopes to an outlet's own warehouses (+ shared/HQ warehouses with no outlet
	// link) when no explicit WarehouseID is given — mirrors the ListItems outlet separation.
	OutletID *uuid.UUID
}

// parseStockLevelFilters reads the shared stock-list query params. explicitOutlet lets a
// caller (the export handler) accept an ?outlet_id= override in addition to the ambient
// X-Outlet-ID header that ListStock relies on.
func parseStockLevelFilters(r *http.Request, explicitOutletParam string) stockLevelFilters {
	q := r.URL.Query()
	f := stockLevelFilters{
		Search:     q.Get("search"),
		LowStock:   q.Get("low_stock") == "true",
		OutOfStock: q.Get("out_of_stock") == "true",
		TypeFilter: strings.ToUpper(strings.TrimSpace(q.Get("type"))),
	}
	if cid, e := uuid.Parse(q.Get("category_id")); e == nil {
		f.CategoryID = &cid
	}
	if wid, e := uuid.Parse(q.Get("warehouse_id")); e == nil {
		f.WarehouseID = &wid
	}
	if lid, e := uuid.Parse(q.Get("location_id")); e == nil {
		f.LocationID = &lid
	}
	if iid, e := uuid.Parse(q.Get("item_id")); e == nil {
		f.ItemID = &iid
	}
	if f.WarehouseID == nil {
		if oid, e := uuid.Parse(q.Get(explicitOutletParam)); explicitOutletParam != "" && e == nil {
			f.OutletID = &oid
		} else if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
			if oid, e := uuid.Parse(outletStr); e == nil {
				f.OutletID = &oid
			}
		}
	}
	return f
}

// queryStockLevels builds and runs the InventoryBalance query for the given filters,
// returning the enriched DTOs. Single source of truth for "what counts as stock" reused by
// ListStock (JSON) and StockExportPDF (branded PDF/CSV) — see [[feedback_workflow_rules]].
func (h *InventoryExtrasHandler) queryStockLevels(ctx context.Context, tenantID uuid.UUID, f stockLevelFilters) ([]stockLevelDTO, error) {
	// Always constrain to stockable item types so non-stock catalog items
	// (services, vouchers, recipes) never surface on the stock levels list.
	itemPreds := []predicate.Item{entitem.TypeIn(stockableTypes...)}
	if f.CategoryID != nil {
		itemPreds = append(itemPreds, entitem.CategoryID(*f.CategoryID))
	}
	if f.TypeFilter != "" {
		// Narrow to the requested type (still bounded by stockableTypes above).
		itemPreds = append(itemPreds, entitem.TypeEQ(entitem.Type(f.TypeFilter)))
	}

	balQuery := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
		Where(entinventorybalance.HasItemWith(itemPreds...)).
		WithItem(func(iq *ent.ItemQuery) { iq.WithItemCategory() }).
		WithWarehouse().
		WithLocation()
	if f.LocationID != nil {
		balQuery = balQuery.Where(entinventorybalance.LocationID(*f.LocationID))
	}
	if f.ItemID != nil {
		balQuery = balQuery.Where(entinventorybalance.ItemID(*f.ItemID))
	}
	if f.WarehouseID != nil {
		balQuery = balQuery.Where(entinventorybalance.WarehouseID(*f.WarehouseID))
	} else if f.OutletID != nil {
		// Outlet drill-down (X-Outlet-ID or explicit ?outlet_id=) with no explicit warehouse
		// filter: scope the stock list to the selected outlet's warehouses (+ shared/HQ
		// warehouses with no outlet link) — selecting a branch anywhere in the app must show
		// that branch's stock, not the whole tenant's.
		balQuery = balQuery.Where(entinventorybalance.HasWarehouseWith(
			entwarehouse.Or(
				entwarehouse.OutletIDEQ(*f.OutletID),
				entwarehouse.OutletIDIsNil(),
			),
		))
	}

	balances, err := balQuery.All(ctx)
	if err != nil {
		return nil, err
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
		locationName := ""
		if b.Edges.Location != nil {
			locationName = b.Edges.Location.Name
		}
		if f.Search != "" {
			needle := strings.ToLower(f.Search)
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
		if f.OutOfStock && !isOut {
			continue
		}
		if f.LowStock && !isLow {
			continue
		}

		result = append(result, stockLevelDTO{
			ID:            b.ID,
			ItemName:      itemName,
			SKU:           sku,
			WarehouseName: warehouseName,
			WarehouseID:   b.WarehouseID,
			LocationID:    b.LocationID,
			LocationName:  locationName,
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
	return result, nil
}

func (h *InventoryExtrasHandler) ListStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	result, err := h.queryStockLevels(r.Context(), tenantID, parseStockLevelFilters(r, ""))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list stock")
		return
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
