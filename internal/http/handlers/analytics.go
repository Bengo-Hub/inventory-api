package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entstockadjustment "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

// AnalyticsHandler serves read-only dashboard analytics endpoints.
type AnalyticsHandler struct {
	log      *zap.Logger
	orm      *ent.Client
	itemsSvc *items.Service
}

func NewAnalyticsHandler(log *zap.Logger, orm *ent.Client) *AnalyticsHandler {
	return &AnalyticsHandler{
		log: log.Named("analytics.handler"),
		orm: orm,
	}
}

// SetItemsService wires items.Service so analytics can reuse items.OutletScope — the same
// outlet-visibility rule the Products list uses — instead of re-deriving it and silently
// drifting from what the catalog page shows for the same outlet.
func (h *AnalyticsHandler) SetItemsService(svc *items.Service) {
	h.itemsSvc = svc
}

// outletFilter resolves the active outlet for this request: an explicit ?outlet_id= override
// takes precedence, falling back to the ambient X-Outlet-ID header (set tenant-wide by
// OutletContext middleware for HQ users drilling into a branch, or forced for branch staff).
// Returns nil when no outlet is selected (tenant-wide view).
func outletFilter(r *http.Request) *uuid.UUID {
	if v := r.URL.Query().Get("outlet_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			return &id
		}
	}
	if v := invmiddleware.GetOutletID(r.Context()); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			return &id
		}
	}
	return nil
}

// outletWarehouseIDSlice flattens items.OutletScope's warehouse set into a slice for use in
// WarehouseIDIn predicates. Returns nil (no filter) when outletID is nil.
func (h *AnalyticsHandler) outletWarehouseIDSlice(ctx context.Context, tenantID uuid.UUID, outletID *uuid.UUID) ([]uuid.UUID, error) {
	if outletID == nil || h.itemsSvc == nil {
		return nil, nil
	}
	_, _, warehouseIDs, err := h.itemsSvc.OutletScope(ctx, tenantID, outletID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(warehouseIDs))
	for id := range warehouseIDs {
		ids = append(ids, id)
	}
	return ids, nil
}

func (h *AnalyticsHandler) RegisterRoutes(r chi.Router) {
	r.Get("/inventory/analytics/top-items", h.TopItems)
	r.Get("/inventory/analytics/stock-trends", h.StockTrends)
	r.Get("/inventory/analytics/distribution", h.Distribution)
	// Reorder/low-stock alerts are the `stock_alerts` plan feature (excluded from
	// use-case PowerSuite tier 1) — gated even though it is a read: the endpoint IS
	// the feature, same as the gated report_* endpoints.
	r.With(authclient.RequireFeatureCode("stock_alerts")).
		Get("/inventory/analytics/reorder-alerts", h.ReorderAlerts)
	r.Get("/inventory/analytics/summary", h.EnhancedSummary)
}

// ─── DTOs ─────────────────────────────────────────────────────────────────────

type topItemDTO struct {
	ItemID     uuid.UUID `json:"item_id"`
	SKU        string    `json:"sku"`
	ItemName   string    `json:"item_name"`
	Category   string    `json:"category"`
	UnitsMoved float64   `json:"units_moved"`
}

type stockTrendPoint struct {
	Date           string  `json:"date"`
	TotalUnits     float64 `json:"total_units"`
	AdjustmentCount int    `json:"adjustment_count"`
}

type categoryDistribution struct {
	CategoryID   *uuid.UUID `json:"category_id"`
	CategoryName string     `json:"category_name"`
	ItemCount    int        `json:"item_count"`
	TotalUnits   float64    `json:"total_units"`
	Percentage   float64    `json:"percentage"`
}

type reorderAlertDTO struct {
	ItemID        uuid.UUID `json:"item_id"`
	SKU           string    `json:"sku"`
	ItemName      string    `json:"item_name"`
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	WarehouseName string    `json:"warehouse_name"`
	CurrentQty    float64   `json:"current_qty"`
	ReorderLevel  int       `json:"reorder_level"`
	Deficit       float64   `json:"deficit"`
}

type enhancedSummaryDTO struct {
	TotalItems         int     `json:"total_items"`
	LowStockCount      int     `json:"low_stock_count"`
	OutOfStockCount    int     `json:"out_of_stock_count"`
	WarehouseCount     int     `json:"warehouse_count"`
	PendingPOCount     int     `json:"pending_po_count"`
	TotalInventoryValue float64 `json:"total_inventory_value"`
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// TopItems returns the top N items by absolute stock movement in the last D days.
// GET /inventory/analytics/top-items?limit=10&days=30
func (h *AnalyticsHandler) TopItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant ID")
		return
	}

	limit := 10
	days := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 && n <= 365 {
			days = n
		}
	}

	since := time.Now().UTC().AddDate(0, 0, -days)

	outletID := outletFilter(r)
	warehouseIDs, err := h.outletWarehouseIDSlice(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("analytics: top items outlet scope failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to resolve outlet scope")
		return
	}

	adjQuery := h.orm.StockAdjustment.Query().
		Where(
			entstockadjustment.TenantID(tenantID),
			entstockadjustment.AdjustedAtGTE(since),
		)
	if warehouseIDs != nil {
		adjQuery = adjQuery.Where(entstockadjustment.WarehouseIDIn(warehouseIDs...))
	}
	adjs, err := adjQuery.All(r.Context())
	if err != nil {
		h.log.Error("analytics: top items query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to fetch adjustments")
		return
	}

	// Aggregate by item_id
	type agg struct {
		itemID uuid.UUID
		moved  float64
	}
	totals := map[uuid.UUID]float64{}
	for _, a := range adjs {
		change := a.QuantityChange
		if change < 0 {
			change = -change
		}
		totals[a.ItemID] += change
	}

	// Sort descending
	aggList := make([]agg, 0, len(totals))
	for id, moved := range totals {
		aggList = append(aggList, agg{itemID: id, moved: moved})
	}
	sort.Slice(aggList, func(i, j int) bool { return aggList[i].moved > aggList[j].moved })
	if len(aggList) > limit {
		aggList = aggList[:limit]
	}

	// Fetch item details
	itemIDs := make([]uuid.UUID, 0, len(aggList))
	for _, a := range aggList {
		itemIDs = append(itemIDs, a.itemID)
	}

	items, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).
		WithItemCategory().
		All(r.Context())
	if err != nil {
		h.log.Warn("analytics: top items — item details failed", zap.Error(err))
	}

	itemMap := map[uuid.UUID]*ent.Item{}
	for _, it := range items {
		itemMap[it.ID] = it
	}

	result := make([]topItemDTO, 0, len(aggList))
	for _, a := range aggList {
		dto := topItemDTO{ItemID: a.itemID, UnitsMoved: a.moved}
		if it, ok := itemMap[a.itemID]; ok {
			dto.SKU = it.Sku
			dto.ItemName = it.Name
			if it.Edges.ItemCategory != nil {
				dto.Category = it.Edges.ItemCategory.Name
			}
		}
		result = append(result, dto)
	}

	writeJSON(w, http.StatusOK, result)
}

// StockTrends returns daily net stock movement for the last D days.
// GET /inventory/analytics/stock-trends?days=30
func (h *AnalyticsHandler) StockTrends(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant ID")
		return
	}

	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 && n <= 365 {
			days = n
		}
	}

	since := time.Now().UTC().AddDate(0, 0, -days)

	outletID := outletFilter(r)
	warehouseIDs, err := h.outletWarehouseIDSlice(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("analytics: stock trends outlet scope failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to resolve outlet scope")
		return
	}

	trendQuery := h.orm.StockAdjustment.Query().
		Where(
			entstockadjustment.TenantID(tenantID),
			entstockadjustment.AdjustedAtGTE(since),
		)
	if warehouseIDs != nil {
		trendQuery = trendQuery.Where(entstockadjustment.WarehouseIDIn(warehouseIDs...))
	}
	adjs, err := trendQuery.
		Order(ent.Asc(entstockadjustment.FieldAdjustedAt)).
		All(r.Context())
	if err != nil {
		h.log.Error("analytics: stock trends query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to fetch trends")
		return
	}

	// Bucket by date
	type bucket struct {
		net   float64
		count int
	}
	buckets := map[string]*bucket{}
	for _, a := range adjs {
		day := a.AdjustedAt.Format("2006-01-02")
		if _, ok := buckets[day]; !ok {
			buckets[day] = &bucket{}
		}
		buckets[day].net += a.QuantityChange
		buckets[day].count++
	}

	// Fill all days in range (including zeros)
	result := make([]stockTrendPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		pt := stockTrendPoint{Date: day}
		if b, ok := buckets[day]; ok {
			pt.TotalUnits = b.net
			pt.AdjustmentCount = b.count
		}
		result = append(result, pt)
	}

	writeJSON(w, http.StatusOK, result)
}

// Distribution returns inventory distribution by category.
// GET /inventory/analytics/distribution
func (h *AnalyticsHandler) Distribution(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant ID")
		return
	}

	outletID := outletFilter(r)
	warehouseIDs, err := h.outletWarehouseIDSlice(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("analytics: distribution outlet scope failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to resolve outlet scope")
		return
	}

	distQuery := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID))
	if warehouseIDs != nil {
		distQuery = distQuery.Where(entinventorybalance.WarehouseIDIn(warehouseIDs...))
	}
	balances, err := distQuery.
		WithItem(func(q *ent.ItemQuery) { q.WithItemCategory() }).
		All(r.Context())
	if err != nil {
		h.log.Error("analytics: distribution query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to fetch distribution")
		return
	}

	type catBucket struct {
		id    *uuid.UUID
		name  string
		items map[uuid.UUID]struct{}
		units float64
	}
	cats := map[string]*catBucket{}

	for _, b := range balances {
		key := "uncategorised"
		catBkt := &catBucket{name: "Uncategorised", items: map[uuid.UUID]struct{}{}}
		if b.Edges.Item != nil && b.Edges.Item.Edges.ItemCategory != nil {
			cat := b.Edges.Item.Edges.ItemCategory
			key = cat.ID.String()
			catID := cat.ID
			catBkt = &catBucket{id: &catID, name: cat.Name, items: map[uuid.UUID]struct{}{}}
		}
		if existing, ok := cats[key]; ok {
			catBkt = existing
		} else {
			cats[key] = catBkt
		}
		if b.Edges.Item != nil {
			catBkt.items[b.Edges.Item.ID] = struct{}{}
		}
		catBkt.units += b.Available
	}

	// Compute totals for percentage
	totalUnits := 0.0
	for _, c := range cats {
		totalUnits += c.units
	}

	result := make([]categoryDistribution, 0, len(cats))
	for _, c := range cats {
		pct := 0.0
		if totalUnits > 0 {
			pct = float64(c.units) / float64(totalUnits) * 100
		}
		result = append(result, categoryDistribution{
			CategoryID:   c.id,
			CategoryName: c.name,
			ItemCount:    len(c.items),
			TotalUnits:   c.units,
			Percentage:   pct,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TotalUnits > result[j].TotalUnits })

	writeJSON(w, http.StatusOK, result)
}

// ReorderAlerts returns all items currently below their reorder level.
// GET /inventory/analytics/reorder-alerts
func (h *AnalyticsHandler) ReorderAlerts(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant ID")
		return
	}

	outletID := outletFilter(r)
	warehouseIDs, err := h.outletWarehouseIDSlice(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("analytics: reorder alerts outlet scope failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to resolve outlet scope")
		return
	}

	alertQuery := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID))
	if warehouseIDs != nil {
		alertQuery = alertQuery.Where(entinventorybalance.WarehouseIDIn(warehouseIDs...))
	}
	balances, err := alertQuery.
		WithItem().
		WithWarehouse().
		All(r.Context())
	if err != nil {
		h.log.Error("analytics: reorder alerts query failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to fetch reorder alerts")
		return
	}

	result := make([]reorderAlertDTO, 0)
	for _, b := range balances {
		if b.ReorderLevel <= 0 || b.Available > float64(b.ReorderLevel) {
			continue
		}
		dto := reorderAlertDTO{
			CurrentQty:   b.Available,
			ReorderLevel: b.ReorderLevel,
			Deficit:      float64(b.ReorderLevel) - b.Available,
			WarehouseID:  b.WarehouseID,
			ItemID:       b.ItemID,
		}
		if b.Edges.Item != nil {
			dto.SKU = b.Edges.Item.Sku
			dto.ItemName = b.Edges.Item.Name
		}
		if b.Edges.Warehouse != nil {
			dto.WarehouseName = b.Edges.Warehouse.Name
		}
		result = append(result, dto)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Deficit > result[j].Deficit })

	writeJSON(w, http.StatusOK, result)
}

// EnhancedSummary returns an enhanced dashboard summary including pending PO count.
// GET /inventory/analytics/summary
func (h *AnalyticsHandler) EnhancedSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant ID")
		return
	}

	outletID := outletFilter(r)

	// TotalItems must agree with the Products list for the same outlet, or the dashboard and
	// the catalog page tell the tenant two different stories about the same branch — reuse the
	// exact same visibility rule (items.OutletScope) rather than re-deriving it here.
	itemQuery := h.orm.Item.Query().Where(entitem.TenantID(tenantID), entitem.IsActive(true))
	var outletExcludeIDs []uuid.UUID
	var hasOperationalHistory bool
	var warehouseIDs []uuid.UUID
	if outletID != nil && h.itemsSvc != nil {
		var whSet map[uuid.UUID]struct{}
		var scopeErr error
		outletExcludeIDs, hasOperationalHistory, whSet, scopeErr = h.itemsSvc.OutletScope(ctx, tenantID, outletID)
		if scopeErr != nil {
			h.log.Error("analytics: summary outlet scope failed", zap.Error(scopeErr))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to resolve outlet scope")
			return
		}
		warehouseIDs = make([]uuid.UUID, 0, len(whSet))
		for id := range whSet {
			warehouseIDs = append(warehouseIDs, id)
		}
		if len(outletExcludeIDs) > 0 {
			itemQuery = itemQuery.Where(entitem.IDNotIn(outletExcludeIDs...))
		}
		if hasOperationalHistory {
			itemQuery = itemQuery.Where(entitem.Or(
				entitem.Not(entitem.TypeIn(entitem.TypeGOODS, entitem.TypeINGREDIENT)),
				entitem.NonBillable(true),
				entitem.HasBalances(),
			))
		}
	}
	totalItems, _ := itemQuery.Count(ctx)

	// Low/out-of-stock tallies must only consider PHYSICAL, stock-bearing item types. SERVICE,
	// RECIPE and VOUCHER items hold no tracked stock (a facial/massage/event ticket is never
	// "out of stock"), so counting their zero-balance rows wildly inflated the dashboard's
	// "Out of Stock" figure. Restrict the stock-status set to GOODS/INGREDIENT/EQUIPMENT.
	activeItemIDs, _ := h.orm.Item.Query().
		Where(
			entitem.TenantID(tenantID),
			entitem.IsActive(true),
			entitem.TypeIn(entitem.TypeGOODS, entitem.TypeINGREDIENT, entitem.TypeEQUIPMENT),
		).
		IDs(ctx)

	var lowStock, outOfStock int
	if len(activeItemIDs) > 0 {
		lowStockQuery := h.orm.InventoryBalance.Query().
			Where(
				entinventorybalance.TenantID(tenantID),
				entinventorybalance.ItemIDIn(activeItemIDs...),
				entinventorybalance.AvailableGT(0),
				entinventorybalance.ReorderLevelGT(0),
				// available <= reorder_level (column-to-column comparison)
				func(s *entsql.Selector) {
					s.Where(entsql.ColumnsLTE(s.C(entinventorybalance.FieldAvailable), s.C(entinventorybalance.FieldReorderLevel)))
				},
			)
		outOfStockQuery := h.orm.InventoryBalance.Query().
			Where(
				entinventorybalance.TenantID(tenantID),
				entinventorybalance.ItemIDIn(activeItemIDs...),
				entinventorybalance.AvailableLTE(0),
			)
		if outletID != nil {
			lowStockQuery = lowStockQuery.Where(entinventorybalance.WarehouseIDIn(warehouseIDs...))
			outOfStockQuery = outOfStockQuery.Where(entinventorybalance.WarehouseIDIn(warehouseIDs...))
		}
		lowStock, _ = lowStockQuery.Count(ctx)
		outOfStock, _ = outOfStockQuery.Count(ctx)
	}

	// Pending POs: draft + sent + partially_received, scoped to the outlet's receiving
	// warehouse(s) when an outlet is selected. A draft PO with no warehouse resolved yet
	// (destination undecided) never counts toward any single outlet's figure.
	poQuery := h.orm.PurchaseOrder.Query().
		Where(
			entpurchaseorder.TenantID(tenantID),
			entpurchaseorder.StatusIn(
				entpurchaseorder.StatusDraft,
				entpurchaseorder.StatusSent,
				entpurchaseorder.StatusPartiallyReceived,
			),
		)
	if outletID != nil {
		poQuery = poQuery.Where(entpurchaseorder.WarehouseIDIn(warehouseIDs...))
	}
	pendingPOs, _ := poQuery.Count(ctx)

	writeJSON(w, http.StatusOK, enhancedSummaryDTO{
		TotalItems:      totalItems,
		LowStockCount:   lowStock,
		OutOfStockCount: outOfStock,
		WarehouseCount:  0, // populated separately by /inventory/summary
		PendingPOCount:  pendingPOs,
	})
}
