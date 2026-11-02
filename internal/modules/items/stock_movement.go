package items

import (
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// The Deadstock and Fast-Moving reports read stock movement straight from Postgres: consumption
// lines are unnested and grouped per SKU in the database (consumption_tenant_id_created_at bounds
// the scan to the tenant's window), and only the report rows come back, never every consumption
// a tenant ever recorded.

// SetReportDB wires the *sql.DB the movement reports aggregate with (the same database as client).
func (s *Service) SetReportDB(db *sql.DB) { s.reportDB = db }

var errReportDBMissing = errors.New("stock movement reports: report database not configured")

// usageReasons are the consumption reasons that count as genuine outbound MOVEMENT of stock, the
// item actually being used up rather than sitting idle. This deliberately spans both demand channels:
//   - sale / pos_sale: finished goods sold to a customer
//   - production: raw materials / ingredients issued to a manufacturing batch (BOM)
//   - conference_meal: ingredients drawn down for conference/event meal cards
//
// Raw materials are never "sold", so a sales-only definition wrongly flags every ingredient as
// deadstock even when it is heavily consumed in production. Reasons like opening_balance /
// initial_count (stock-in) and purchase_return (reversal) are intentionally excluded: they are not
// demand-driven usage.
var usageReasons = []string{"sale", "pos_sale", "production", "conference_meal"}

// movementCTE groups usage consumption lines per SKU since a floor date: when the SKU last moved
// and how much moved from the window start on. Parameters: $1 tenant, $2 usage reasons, $3 window
// start, $4 floor, $5 warehouse ids (NULL means every warehouse). A scoped report counts only
// movement out of its own warehouses, so an item selling at one outlet still shows as dead stock
// at another.
const movementCTE = `moved AS (
	SELECT upper(btrim(line->>'sku')) AS sku,
	       max(c.created_at) AS last_used,
	       count(*) FILTER (WHERE c.created_at >= $3) AS lines_in_window,
	       coalesce(sum((line->>'quantity')::float8) FILTER (WHERE c.created_at >= $3), 0) AS qty_in_window
	FROM consumptions c
	CROSS JOIN LATERAL jsonb_array_elements(c.items) AS line
	WHERE c.tenant_id = $1
	  AND c.reason = ANY($2::text[])
	  AND c.created_at >= $4
	  AND ($5::uuid[] IS NULL OR c.warehouse_id = ANY($5::uuid[]))
	  AND btrim(coalesce(line->>'sku', '')) <> ''
	GROUP BY 1
)`

// stockCTE sums on-hand per item over the scoped warehouses ($1 tenant, $5 warehouse ids):
// on_hand is the net figure, held counts only the warehouses holding stock (a backordered,
// negative location never cancels out stock sitting idle at another).
const stockCTE = `stock AS (
	SELECT b.item_id,
	       sum(b.on_hand) AS on_hand,
	       coalesce(sum(b.on_hand) FILTER (WHERE b.on_hand > 0), 0) AS held,
	       max(b.updated_at) FILTER (WHERE b.on_hand > 0) AS last_change
	FROM inventory_balances b
	WHERE b.tenant_id = $1
	  AND ($5::uuid[] IS NULL OR b.warehouse_id = ANY($5::uuid[]))
	GROUP BY b.item_id
)`

// warehouseParam renders a warehouse scope for $5: NULL for a nil scope (every warehouse), else
// the ids as text. Callers return an empty report for a non-nil empty scope (a location with no
// warehouse) before querying, so it never widens to the whole tenant.
func warehouseParam(ids []uuid.UUID) any {
	if ids == nil {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
