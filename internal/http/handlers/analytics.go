package handlers

import (
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
)

// AnalyticsHandler serves read-only dashboard analytics endpoints.
type AnalyticsHandler struct {
	log *zap.Logger
	orm *ent.Client
}

func NewAnalyticsHandler(log *zap.Logger, orm *ent.Client) *AnalyticsHandler {
	return &AnalyticsHandler{
		log: log.Named("analytics.handler"),
		orm: orm,
	}
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

	adjs, err := h.orm.StockAdjustment.Query().
		Where(
			entstockadjustment.TenantID(tenantID),
			entstockadjustment.AdjustedAtGTE(since),
		).
		All(r.Context())
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

	adjs, err := h.orm.StockAdjustment.Query().
		Where(
			entstockadjustment.TenantID(tenantID),
			entstockadjustment.AdjustedAtGTE(since),
		).
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

	balances, err := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
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

	balances, err := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
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

	totalItems, _ := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IsActive(true)).
		Count(ctx)

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
		// Low stock: available > 0 AND available <= reorder_level AND reorder_level > 0
		// Uses a column-to-column comparison via raw predicate.
		lowStock, _ = h.orm.InventoryBalance.Query().
			Where(
				entinventorybalance.TenantID(tenantID),
				entinventorybalance.ItemIDIn(activeItemIDs...),
				entinventorybalance.AvailableGT(0),
				entinventorybalance.ReorderLevelGT(0),
				// available <= reorder_level (column-to-column comparison)
				func(s *entsql.Selector) {
					s.Where(entsql.ColumnsLTE(s.C(entinventorybalance.FieldAvailable), s.C(entinventorybalance.FieldReorderLevel)))
				},
			).
			Count(ctx)

		outOfStock, _ = h.orm.InventoryBalance.Query().
			Where(
				entinventorybalance.TenantID(tenantID),
				entinventorybalance.ItemIDIn(activeItemIDs...),
				entinventorybalance.AvailableLTE(0),
			).
			Count(ctx)
	}

	// Pending POs: draft + sent + partially_received
	pendingPOs, _ := h.orm.PurchaseOrder.Query().
		Where(
			entpurchaseorder.TenantID(tenantID),
			entpurchaseorder.StatusIn(
				entpurchaseorder.StatusDraft,
				entpurchaseorder.StatusSent,
				entpurchaseorder.StatusPartiallyReceived,
			),
		).
		Count(ctx)

	writeJSON(w, http.StatusOK, enhancedSummaryDTO{
		TotalItems:      totalItems,
		LowStockCount:   lowStock,
		OutOfStockCount: outOfStock,
		WarehouseCount:  0, // populated separately by /inventory/summary
		PendingPOCount:  pendingPOs,
	})
}
