-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "prices_inclusive_of_tax" boolean NOT NULL DEFAULT false, ADD COLUMN "default_tax_code" character varying NULL;
