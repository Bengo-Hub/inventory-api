package items

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DeadstockItem is a single non-moving stock line.
type DeadstockItem struct {
	ItemID       uuid.UUID `json:"item_id"`
	SKU          string    `json:"sku"`
	Name         string    `json:"name"`
	CategoryName string    `json:"category_name,omitempty"`
	BrandName    string    `json:"brand_name,omitempty"`
	OnHand       float64   `json:"on_hand"`
	UnitCost     float64   `json:"unit_cost"`
	Value        float64   `json:"value"`
	// LastActivity is the last sale or usage; for stock never used it is the last stock change.
	LastActivity time.Time `json:"last_activity"`
	// NeverUsed is true when no sale or usage was ever recorded for the item in scope.
	NeverUsed bool `json:"never_used"`
	// HeldSince is when the oldest unit still in stock arrived (oldest active lot), else when the
	// item was created.
	HeldSince time.Time `json:"held_since"`
	// DaysHeld is whole days since HeldSince.
	DaysHeld int `json:"days_held"`
}

// DeadstockReport lists capital tied up in stock that has not sold within the lookback window.
type DeadstockReport struct {
	Days           int     `json:"days"`
	Currency       string  `json:"currency"`
	ItemCount      int     `json:"item_count"`
	TotalDeadValue float64 `json:"total_dead_value"`
	// TooNewCount and TooNewValue cover idle stock that arrived inside the window: it has not
	// had the full window to sell yet, so it is not called dead.
	TooNewCount int     `json:"too_new_count"`
	TooNewValue float64 `json:"too_new_value"`
	// HistorySince is the earliest stock record in scope: before it there is no data, so a window
	// reaching further back than this is only partly covered. Nil when nothing is stocked.
	HistorySince *time.Time      `json:"history_since,omitempty"`
	Offset       int             `json:"offset"`
	Limit        int             `json:"limit"`
	Items        []DeadstockItem `json:"items"`
}

// deadstockLimit is the default page of listed lines; ItemCount and TotalDeadValue still cover
// every dead item.
const deadstockLimit = 100

// deadstockLookback bounds the movement scan: an item not used in two years shows its last stock
// change as its last activity instead.
const deadstockLookback = 2

// StockDeadstock lists items held in stock whose SKU has had no outbound movement (usageReasons)
// within the last `days` days: capital tied up in non-moving stock. Production and recipe
// consumption count as movement, so actively used raw materials are not reported as dead.
//
// An item is only judged once its stock has been held for the whole window: stock that arrived
// inside the window is counted as "too new" instead (owner, 2026-11-18). A tenant that started
// recently would otherwise see every newly received item as dead under a 90 day window.
//
// Value = held stock x cost_price. warehouseIDs scopes stock, lots and movement to those
// warehouses (an outlet's, or one location); nil means every warehouse. f narrows by category,
// brand and search and pages the list; counts and totals cover every match.
func (s *Service) StockDeadstock(ctx context.Context, tenantID uuid.UUID, days int, warehouseIDs []uuid.UUID, f ReportFilter) (*DeadstockReport, error) {
	if days <= 0 {
		days = 90
	}
	offset, limit := f.page(deadstockLimit)
	rep := &DeadstockReport{Days: days, Currency: "KES", Offset: offset, Limit: limit, Items: []DeadstockItem{}}
	if warehouseIDs != nil && len(warehouseIDs) == 0 {
		return rep, nil // a location with no warehouse holds no stock
	}
	if s.reportDB == nil {
		return nil, errReportDBMissing
	}
	now := time.Now()
	windowStart := now.AddDate(0, 0, -days)
	// $1 tenant, $2 reasons, $3 window start, $4 movement floor, $5 warehouses, $6 limit,
	// $7 offset, $8 categories, $9 brands, $10 search.
	rows, err := s.reportDB.QueryContext(ctx, `WITH `+movementCTE+`, `+stockCTE+`,
held AS (
	SELECT l.item_id, min(coalesce(l.received_at, l.created_at)) AS since
	FROM inventory_lots l
	WHERE l.tenant_id = $1 AND l.status = 'active' AND l.quantity > 0
	  AND ($5::uuid[] IS NULL OR l.warehouse_id = ANY($5::uuid[]))
	GROUP BY l.item_id
),
adj AS (
	-- Per location still holding stock: when its current run of stock began (the last restock
	-- from empty), else its first stock-in; the item's run is the oldest of those.
	SELECT a.item_id, min(coalesce(a.restocked_at, a.first_in)) AS since
	FROM (
		SELECT a.item_id, a.warehouse_id,
		       max(a.adjusted_at) FILTER (WHERE a.quantity_before <= 0 AND a.quantity_after > 0) AS restocked_at,
		       min(a.adjusted_at) FILTER (WHERE a.quantity_change > 0) AS first_in
		FROM stock_adjustments a
		WHERE a.tenant_id = $1 AND ($5::uuid[] IS NULL OR a.warehouse_id = ANY($5::uuid[]))
		GROUP BY 1, 2
	) a
	JOIN inventory_balances b ON b.tenant_id = $1 AND b.item_id = a.item_id AND b.warehouse_id = a.warehouse_id AND b.on_hand > 0
	GROUP BY 1
),
track AS (
	-- When stock records begin in scope; nothing before it can be judged.
	SELECT least(
		(SELECT min(c.created_at) FROM consumptions c WHERE c.tenant_id = $1 AND ($5::uuid[] IS NULL OR c.warehouse_id = ANY($5::uuid[]))),
		(SELECT min(a.adjusted_at) FROM stock_adjustments a WHERE a.tenant_id = $1 AND ($5::uuid[] IS NULL OR a.warehouse_id = ANY($5::uuid[]))),
		(SELECT min(coalesce(l.received_at, l.created_at)) FROM inventory_lots l WHERE l.tenant_id = $1 AND ($5::uuid[] IS NULL OR l.warehouse_id = ANY($5::uuid[])))
	) AS since
),
idle AS (
	SELECT i.id, i.sku, i.name, coalesce(cat.name, '') AS category_name, coalesce(br.name, '') AS brand_name,
	       st.held, coalesce(i.cost_price, 0) AS unit_cost, st.held * coalesce(i.cost_price, 0) AS value,
	       coalesce(m.last_used, st.last_change, i.created_at) AS last_activity,
	       m.last_used IS NULL AS never_used,
	       greatest(coalesce(h.since, ad.since, i.created_at), tr.since) AS held_since
	FROM stock st
	JOIN items i ON i.id = st.item_id
	CROSS JOIN track tr
	LEFT JOIN item_categories cat ON cat.id = i.category_id
	LEFT JOIN item_brands br ON br.id = i.brand_id
	LEFT JOIN moved m ON m.sku = upper(btrim(i.sku))
	LEFT JOIN held h ON h.item_id = i.id
	LEFT JOIN adj ad ON ad.item_id = i.id
	WHERE st.held > 0 AND coalesce(m.lines_in_window, 0) = 0
	  AND `+itemFilterSQL("$8", "$9", "$10")+`
),
judged AS (
	SELECT idle.*, held_since <= $3 AS dead FROM idle
),
summary AS (
	SELECT count(*) FILTER (WHERE dead) AS dead_count,
	       coalesce(sum(value) FILTER (WHERE dead), 0) AS dead_value,
	       count(*) FILTER (WHERE NOT dead) AS new_count,
	       coalesce(sum(value) FILTER (WHERE NOT dead), 0) AS new_value,
	       (SELECT since FROM track) AS history_since
	FROM judged
)
SELECT s.dead_count, s.dead_value, s.new_count, s.new_value, s.history_since,
       d.id, d.sku, d.name, d.category_name, d.brand_name, d.held, d.unit_cost, d.value,
       d.last_activity, d.never_used, d.held_since
FROM summary s
LEFT JOIN LATERAL (
	SELECT * FROM judged WHERE dead ORDER BY value DESC, id LIMIT $6 OFFSET $7
) d ON true`,
		tenantID, usageReasons, windowStart, now.AddDate(-deadstockLookback, 0, 0),
		warehouseParam(warehouseIDs), limit, offset,
		uuidParam(f.CategoryIDs), uuidParam(f.BrandIDs), f.searchPattern())
	if err != nil {
		return nil, fmt.Errorf("deadstock: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			history                       sql.NullTime
			id                            uuid.NullUUID
			sku, name, catName, brandName sql.NullString
			held, unitCost, value         sql.NullFloat64
			lastActivity, heldSince       sql.NullTime
			neverUsed                     sql.NullBool
		)
		if err := rows.Scan(&rep.ItemCount, &rep.TotalDeadValue, &rep.TooNewCount, &rep.TooNewValue, &history,
			&id, &sku, &name, &catName, &brandName, &held, &unitCost, &value,
			&lastActivity, &neverUsed, &heldSince); err != nil {
			return nil, fmt.Errorf("deadstock: scan: %w", err)
		}
		if history.Valid && rep.HistorySince == nil {
			t := history.Time
			rep.HistorySince = &t
		}
		if !id.Valid {
			continue // the summary row when no item is dead
		}
		rep.Items = append(rep.Items, DeadstockItem{
			ItemID: id.UUID, SKU: sku.String, Name: name.String, CategoryName: catName.String,
			BrandName: brandName.String, OnHand: held.Float64, UnitCost: unitCost.Float64, Value: value.Float64,
			LastActivity: lastActivity.Time, NeverUsed: neverUsed.Bool, HeldSince: heldSince.Time,
			DaysHeld: int(now.Sub(heldSince.Time).Hours() / 24),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deadstock: rows: %w", err)
	}
	return rep, nil
}
