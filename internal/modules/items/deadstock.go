package items

import (
	"context"
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
	OnHand       float64   `json:"on_hand"`
	UnitCost     float64   `json:"unit_cost"`
	Value        float64   `json:"value"`
	LastActivity time.Time `json:"last_activity"`
}

// DeadstockReport lists capital tied up in stock that has not sold within the lookback window.
type DeadstockReport struct {
	Days           int             `json:"days"`
	Currency       string          `json:"currency"`
	ItemCount      int             `json:"item_count"`
	TotalDeadValue float64         `json:"total_dead_value"`
	Items          []DeadstockItem `json:"items"`
}

// deadstockLimit caps the listed lines; ItemCount and TotalDeadValue still cover every dead item.
const deadstockLimit = 100

// deadstockLookback bounds the movement scan: an item not used in two years shows its last stock
// change as its last activity instead.
const deadstockLookback = 2

// StockDeadstock lists items held in stock whose SKU has had no outbound movement (usageReasons)
// within the last `days` days: capital tied up in non-moving stock. Production and recipe
// consumption count as movement, so actively used raw materials are not reported as dead.
// Value = held stock x cost_price. warehouseIDs scopes stock and movement to those warehouses
// (an outlet's, or one location); nil means every warehouse. Returns the top items by value; the
// count and total cover all of them.
func (s *Service) StockDeadstock(ctx context.Context, tenantID uuid.UUID, days int, warehouseIDs []uuid.UUID) (*DeadstockReport, error) {
	if days <= 0 {
		days = 90
	}
	rep := &DeadstockReport{Days: days, Currency: "KES", Items: []DeadstockItem{}}
	if warehouseIDs != nil && len(warehouseIDs) == 0 {
		return rep, nil // a location with no warehouse holds no stock
	}
	if s.reportDB == nil {
		return nil, errReportDBMissing
	}
	now := time.Now()
	rows, err := s.reportDB.QueryContext(ctx, `WITH `+movementCTE+`, `+stockCTE+`,
dead AS (
	SELECT i.id, i.sku, i.name, coalesce(cat.name, '') AS category_name, st.held,
	       coalesce(i.cost_price, 0) AS unit_cost, st.held * coalesce(i.cost_price, 0) AS value,
	       coalesce(m.last_used, st.last_change) AS last_activity
	FROM stock st
	JOIN items i ON i.id = st.item_id
	LEFT JOIN item_categories cat ON cat.id = i.category_id
	LEFT JOIN moved m ON m.sku = upper(btrim(i.sku))
	WHERE st.held > 0 AND coalesce(m.lines_in_window, 0) = 0
)
SELECT id, sku, name, category_name, held, unit_cost, value, last_activity,
       count(*) OVER () AS item_count, sum(value) OVER () AS total_value
FROM dead
ORDER BY value DESC, id
LIMIT $6`,
		tenantID, usageReasons, now.AddDate(0, 0, -days), now.AddDate(-deadstockLookback, 0, 0),
		warehouseParam(warehouseIDs), deadstockLimit)
	if err != nil {
		return nil, fmt.Errorf("deadstock: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it DeadstockItem
		if err := rows.Scan(&it.ItemID, &it.SKU, &it.Name, &it.CategoryName, &it.OnHand, &it.UnitCost,
			&it.Value, &it.LastActivity, &rep.ItemCount, &rep.TotalDeadValue); err != nil {
			return nil, fmt.Errorf("deadstock: scan: %w", err)
		}
		rep.Items = append(rep.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deadstock: rows: %w", err)
	}
	return rep, nil
}
