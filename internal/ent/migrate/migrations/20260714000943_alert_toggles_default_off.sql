-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ALTER COLUMN "enable_low_stock_notifications" SET DEFAULT false, ALTER COLUMN "enable_expiry_notifications" SET DEFAULT false;
-- Backfill: alert emails are now opt-in. Existing rows carry the old default (true) that no
-- tenant ever consciously chose — reset them so every tenant starts opted out.
UPDATE "tenant_inventory_configs" SET "enable_low_stock_notifications" = false, "enable_expiry_notifications" = false;
