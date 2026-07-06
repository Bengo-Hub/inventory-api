-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "unit_content_qty" double precision NULL, ADD COLUMN "unit_content_uom" character varying NULL, ADD COLUMN "stock_tracking_mode" character varying NOT NULL DEFAULT 'default';
-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "recipe_items_non_depleting_default" boolean NOT NULL DEFAULT false, ADD COLUMN "record_theoretical_usage" boolean NOT NULL DEFAULT true;
