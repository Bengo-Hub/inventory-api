-- Drop two stale unique indexes left over from earlier schema iterations (one predating
-- outlet_id being added to the composite key at all, one predating effective_from). The
-- 2026-07-30 migration (item_pricing_history_index) intended to drop the 4-column
-- (...,outlet_id) constraint, but ent's online auto-migrate never actually executes a
-- checked-in migration file's DROP INDEX (WithDropIndex defaults false) — the file was
-- committed as documentation of intent but never ran against the live database, so BOTH
-- stale constraints silently survived. Either one alone was enough to reject the SECOND price
-- change ever made to an item with SQLSTATE 23505 (duplicate key) — the true root cause of "I
-- changed the price and POS never reflects it": the tier-price upsert step errored out before
-- setSellingPrice ever published its item.updated sync event. Superseded by the 5-column
-- unique index (tenant_id, item_id, pricing_tier_id, outlet_id, effective_from), a strict
-- superset of both. Applied directly to prod via psql on 2026-08-04 (confirmed both indexes
-- existed and were dropped); this file documents that change for the migration history and so
-- a fresh environment reaches the same end state.
DROP INDEX IF EXISTS "itempricing_tenant_id_item_id_pricing_tier_id";
DROP INDEX IF EXISTS "itempricing_tenant_id_item_id_pricing_tier_id_outlet_id";
