package items

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// FastMovingItem is a single high-turnover stock line.
type FastMovingItem struct {
	ItemID        uuid.UUID `json:"item_id"`
	SKU           string    `json:"sku"`
	Name          string    `json:"name"`
	CategoryName  string    `json:"category_name,omitempty"`
	OnHand        float64   `json:"on_hand"`
	QuantityMoved float64   `json:"quantity_moved"`
	LastActivity  time.Time `json:"last_activity"`
}

// FastMovingReport ranks items by outbound movement volume within the lookback window: the
// mirror image of DeadstockReport, using the same usageReasons movement definition so the two
// reports agree on what counts as "moved".
type FastMovingReport struct {
	Days      int              `json:"days"`
	ItemCount int              `json:"item_count"`
	Items     []FastMovingItem `json:"items"`
}

// fastMovingLimit caps the listed lines; ItemCount still covers every item that moved.
const fastMovingLimit = 100

// StockFastMoving ranks stocked items by outbound movement quantity (usageReasons) within the last
// `days` days, busiest first. warehouseIDs scopes stock and movement to those warehouses; nil means
// every warehouse.
func (s *Service) StockFastMoving(ctx context.Context, tenantID uuid.UUID, days int, warehouseIDs []uuid.UUID) (*FastMovingReport, error) {
	if days <= 0 {
		days = 90
	}
	rep := &FastMovingReport{Days: days, Items: []FastMovingItem{}}
	if warehouseIDs != nil && len(warehouseIDs) == 0 {
		return rep, nil
	}
	if s.reportDB == nil {
		return nil, errReportDBMissing
	}
	since := time.Now().AddDate(0, 0, -days)
	rows, err := s.reportDB.QueryContext(ctx, `WITH `+movementCTE+`, `+stockCTE+`
SELECT i.id, i.sku, i.name, coalesce(cat.name, '') AS category_name, st.on_hand, m.qty_in_window,
       m.last_used, count(*) OVER () AS item_count
FROM stock st
JOIN items i ON i.id = st.item_id
JOIN moved m ON m.sku = upper(btrim(i.sku))
LEFT JOIN item_categories cat ON cat.id = i.category_id
ORDER BY m.qty_in_window DESC, i.id
LIMIT $6`,
		tenantID, usageReasons, since, since, warehouseParam(warehouseIDs), fastMovingLimit)
	if err != nil {
		return nil, fmt.Errorf("fastmoving: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it FastMovingItem
		if err := rows.Scan(&it.ItemID, &it.SKU, &it.Name, &it.CategoryName, &it.OnHand, &it.QuantityMoved,
			&it.LastActivity, &rep.ItemCount); err != nil {
			return nil, fmt.Errorf("fastmoving: scan: %w", err)
		}
		rep.Items = append(rep.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fastmoving: rows: %w", err)
	}
	return rep, nil
}
