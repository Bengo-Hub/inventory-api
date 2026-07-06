-- Correct the over-broad ugali non-billable backfill (20260706093044): LIKE '%ugali%'
-- also caught raw ingredients ("Ugali Flour") and would catch PRICED mains such as
-- "Ugali Beef" - zeroing real menu prices. Safe to reset wholesale here: the flag
-- shipped minutes before this migration, so the broad backfill is its only writer.
UPDATE "items" SET "non_billable" = false WHERE "non_billable" = true;
-- Non-billable = the standalone ugali ACCOMPANIMENT only (sellable types, exact names).
UPDATE "items" SET "non_billable" = true
WHERE "type" IN ('RECIPE', 'GOODS')
  AND lower(btrim("name")) IN ('ugali', 'ugali [acc]', 'ugali (acc)', 'ugali accompaniment');
