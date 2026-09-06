-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "per_outlet_pricing_enabled" boolean NOT NULL DEFAULT false;
