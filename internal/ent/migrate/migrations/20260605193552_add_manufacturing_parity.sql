-- Modify "recipes" table
ALTER TABLE "recipes" ADD COLUMN "kind" character varying NOT NULL DEFAULT 'menu';
-- Create "manufacturing_analytics" table
CREATE TABLE "manufacturing_analytics" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "date" character varying NOT NULL, "total_batches" bigint NOT NULL DEFAULT 0, "completed_batches" bigint NOT NULL DEFAULT 0, "failed_batches" bigint NOT NULL DEFAULT 0, "total_production_qty" double precision NOT NULL DEFAULT 0, "total_raw_material_cost" double precision NOT NULL DEFAULT 0, "total_labor_cost" double precision NOT NULL DEFAULT 0, "total_overhead_cost" double precision NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "manufacturinganalytics_tenant_id_date" to table: "manufacturing_analytics"
CREATE UNIQUE INDEX "manufacturinganalytics_tenant_id_date" ON "manufacturing_analytics" ("tenant_id", "date");
-- Create "raw_material_usages" table
CREATE TABLE "raw_material_usages" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "production_batch_id" uuid NULL, "finished_item_id" uuid NULL, "raw_item_id" uuid NOT NULL, "raw_sku" character varying NULL, "quantity" double precision NOT NULL DEFAULT 0, "cost" double precision NOT NULL DEFAULT 0, "transaction_type" character varying NOT NULL DEFAULT 'production', "notes" text NULL, "occurred_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "rawmaterialusage_tenant_id_production_batch_id" to table: "raw_material_usages"
CREATE INDEX "rawmaterialusage_tenant_id_production_batch_id" ON "raw_material_usages" ("tenant_id", "production_batch_id");
-- Create index "rawmaterialusage_tenant_id_raw_item_id" to table: "raw_material_usages"
CREATE INDEX "rawmaterialusage_tenant_id_raw_item_id" ON "raw_material_usages" ("tenant_id", "raw_item_id");
-- Create index "rawmaterialusage_tenant_id_transaction_type" to table: "raw_material_usages"
CREATE INDEX "rawmaterialusage_tenant_id_transaction_type" ON "raw_material_usages" ("tenant_id", "transaction_type");
