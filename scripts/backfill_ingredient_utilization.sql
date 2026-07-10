-- Ingredient Utilization historical backfill
--
-- Best-effort re-derivation of recipe attribution for `consumptions` rows that predate the
-- ConsumptionLine rollout (2026-07-10, commit ad95b32). For each ingredient line in a
-- Consumption's `items` JSONB array:
--   - resolve the ingredient Item by (tenant_id, sku)
--   - find CURRENT active recipes that list it as an ingredient (the recipe may have
--     changed since the sale — this is an approximation, not fact, hence reason='backfill'
--     on every row this script writes, distinguishing it from real-time reason='sale' rows)
--   - split the line's quantity across candidate recipes, weighted by each recipe's own
--     recipe_ingredients.quantity (a proxy for "how central this ingredient is to that
--     recipe" — the real missing variable, how many portions of each recipe were actually
--     sold on that order, isn't available without per-order-line POS data, which this
--     backfill deliberately does not fetch — see the ingredient-utilization plan doc for
--     why that was scoped out)
--   - a line whose ingredient isn't used by ANY active recipe is treated as a direct sale
--     (recipe_id NULL, finished_item_sku = the ingredient's own sku)
--
-- Idempotent: only touches `consumptions` rows that have ZERO existing `consumption_lines`
-- rows, so it is safe to re-run (e.g. after new recipes are added) without double-counting
-- rows already covered by the real-time write path or a prior backfill run.
--
-- To undo: DELETE FROM consumption_lines WHERE reason = 'backfill'; then rebuild
-- item_consumption_dailies from the remaining consumption_lines rows (there is no reverse
-- decrement of the rollup here — see the companion rebuild query at the bottom of this file).
--
-- Run against the `inventory` database as a role with INSERT on both tables, e.g.:
--   kubectl exec -i -n infra postgresql-0 -c postgresql -- \
--     env PGPASSWORD=<admin_user password> psql -U admin_user -d inventory \
--     -f scripts/backfill_ingredient_utilization.sql

BEGIN;

WITH candidate_consumptions AS (
    SELECT c.id, c.tenant_id, c.order_id, c.warehouse_id, c.items, c.processed_at
    FROM consumptions c
    WHERE c.status = 'processed'
      AND NOT EXISTS (SELECT 1 FROM consumption_lines cl WHERE cl.consumption_id = c.id)
),
resolved_warehouse AS (
    SELECT cc.id AS consumption_id,
           COALESCE(cc.warehouse_id, dw.id) AS warehouse_id
    FROM candidate_consumptions cc
    LEFT JOIN LATERAL (
        SELECT id FROM warehouses
        WHERE tenant_id = cc.tenant_id AND is_default = true AND is_active = true
        LIMIT 1
    ) dw ON cc.warehouse_id IS NULL
),
resolved_scope AS (
    SELECT rw.consumption_id, rw.warehouse_id, w.outlet_id
    FROM resolved_warehouse rw
    LEFT JOIN warehouses w ON w.id = rw.warehouse_id
),
exploded_items AS (
    SELECT cc.id AS consumption_id, cc.tenant_id, cc.order_id, cc.processed_at,
           item ->> 'sku' AS sku,
           (item ->> 'quantity')::double precision AS quantity,
           COALESCE((item ->> 'theoretical')::boolean, false) AS theoretical,
           COALESCE((item ->> 'unit_mismatch')::boolean, false) AS unit_mismatch
    FROM candidate_consumptions cc, jsonb_array_elements(cc.items) AS item
),
valid_items AS (
    SELECT ei.*, i.id AS ingredient_item_id, i.cost_price
    FROM exploded_items ei
    JOIN items i ON i.tenant_id = ei.tenant_id AND i.sku = ei.sku
    WHERE ei.quantity > 0 AND ei.unit_mismatch = false
),
recipe_weights AS (
    SELECT ri.item_id AS ingredient_item_id, ri.recipe_id, r.sku AS recipe_sku,
           ri.quantity AS weight,
           SUM(ri.quantity) OVER (PARTITION BY ri.item_id) AS total_weight
    FROM recipe_ingredients ri
    JOIN recipes r ON r.id = ri.recipe_id
    WHERE r.is_active = true
),
attributed AS (
    -- Ingredient used by at least one active recipe: proportional split.
    SELECT vi.consumption_id, vi.tenant_id, vi.order_id, vi.processed_at,
           vi.ingredient_item_id, vi.sku AS ingredient_sku, vi.cost_price, vi.theoretical,
           rw.recipe_id, rw.recipe_sku,
           vi.quantity * (rw.weight / rw.total_weight) AS quantity
    FROM valid_items vi
    JOIN recipe_weights rw ON rw.ingredient_item_id = vi.ingredient_item_id
    UNION ALL
    -- Ingredient not used by any active recipe: direct sale (the ingredient IS the finished item).
    SELECT vi.consumption_id, vi.tenant_id, vi.order_id, vi.processed_at,
           vi.ingredient_item_id, vi.sku AS ingredient_sku, vi.cost_price, vi.theoretical,
           NULL::uuid AS recipe_id, NULL::character varying AS recipe_sku,
           vi.quantity
    FROM valid_items vi
    WHERE NOT EXISTS (SELECT 1 FROM recipe_weights rw WHERE rw.ingredient_item_id = vi.ingredient_item_id)
),
inserted AS (
    INSERT INTO consumption_lines (
        id, tenant_id, consumption_id, order_id, warehouse_id, outlet_id,
        recipe_id, recipe_sku, finished_item_sku, ingredient_item_id, ingredient_sku,
        quantity, unit_cost, total_cost, theoretical, reason, consumed_at, created_at
    )
    SELECT
        gen_random_uuid(), a.tenant_id, a.consumption_id, a.order_id, rs.warehouse_id, rs.outlet_id,
        a.recipe_id, a.recipe_sku, COALESCE(a.recipe_sku, a.ingredient_sku), a.ingredient_item_id, a.ingredient_sku,
        a.quantity, COALESCE(a.cost_price, 0), a.quantity * COALESCE(a.cost_price, 0),
        a.theoretical, 'backfill', a.processed_at, now()
    FROM attributed a
    JOIN resolved_scope rs ON rs.consumption_id = a.consumption_id
    WHERE rs.warehouse_id IS NOT NULL
    RETURNING tenant_id, warehouse_id, outlet_id, recipe_id, recipe_sku, ingredient_item_id, ingredient_sku, quantity, total_cost, consumed_at
)
INSERT INTO item_consumption_dailies (
    id, tenant_id, warehouse_id, outlet_id, item_id, item_sku, recipe_id, recipe_sku,
    bucket_date, quantity, total_cost, updated_at
)
SELECT gen_random_uuid(), tenant_id, warehouse_id, outlet_id, ingredient_item_id, ingredient_sku,
       COALESCE(recipe_id, '00000000-0000-0000-0000-000000000000'::uuid), recipe_sku,
       date_trunc('day', consumed_at), SUM(quantity), SUM(total_cost), now()
FROM inserted
GROUP BY tenant_id, warehouse_id, outlet_id, ingredient_item_id, ingredient_sku, recipe_id, recipe_sku, date_trunc('day', consumed_at)
ON CONFLICT (tenant_id, warehouse_id, item_id, recipe_id, bucket_date)
DO UPDATE SET quantity = item_consumption_dailies.quantity + EXCLUDED.quantity,
              total_cost = item_consumption_dailies.total_cost + EXCLUDED.total_cost,
              updated_at = now();

COMMIT;

-- ---------------------------------------------------------------------------------------
-- Dry-run preview (run this FIRST, standalone, no transaction/writes) — swap the final
-- INSERT/RETURNING above for a plain SELECT count(*)/sum(quantity) grouped however you want
-- to sanity-check volume before committing. Example:
--
-- SELECT count(*) AS lines_would_insert, count(DISTINCT consumption_id) AS consumptions_covered
-- FROM (<paste the `attributed` CTE query above, ending at `attributed`, then `SELECT * FROM attributed`>) x;
-- ---------------------------------------------------------------------------------------
