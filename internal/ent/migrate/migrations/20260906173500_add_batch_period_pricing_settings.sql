-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "batch_period_pricing_enabled" boolean NOT NULL DEFAULT false, ADD COLUMN "stock_aging_threshold_days" bigint NOT NULL DEFAULT 90;
