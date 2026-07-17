-- Modify "stock_count_lines" table
ALTER TABLE "stock_count_lines" ADD COLUMN "reason" character varying NULL;
-- Create "stock_count_templates" table
CREATE TABLE "stock_count_templates" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "description" text NULL, "warehouse_id" uuid NULL, "item_ids" jsonb NULL, "category_ids" jsonb NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockcounttemplate_tenant_id_is_active" to table: "stock_count_templates"
CREATE INDEX "stockcounttemplate_tenant_id_is_active" ON "stock_count_templates" ("tenant_id", "is_active");
-- Create index "stockcounttemplate_tenant_id_name" to table: "stock_count_templates"
CREATE UNIQUE INDEX "stockcounttemplate_tenant_id_name" ON "stock_count_templates" ("tenant_id", "name");
