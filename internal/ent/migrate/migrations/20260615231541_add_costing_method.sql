-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "costing_method" character varying NOT NULL DEFAULT 'wavg';
