-- Create "stock_breakdowns" table
CREATE TABLE "stock_breakdowns" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "parent_item_id" uuid NOT NULL, "parent_sku" character varying NOT NULL, "child_item_id" uuid NOT NULL, "child_sku" character varying NOT NULL, "warehouse_id" uuid NOT NULL, "parent_quantity" double precision NOT NULL, "child_quantity" double precision NOT NULL, "conversion_factor" double precision NOT NULL, "cost_allocated" double precision NULL, "reference" character varying NULL, "notes" text NULL, "created_by" uuid NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockbreakdown_created_at" to table: "stock_breakdowns"
CREATE INDEX "stockbreakdown_created_at" ON "stock_breakdowns" ("created_at");
-- Create index "stockbreakdown_tenant_id" to table: "stock_breakdowns"
CREATE INDEX "stockbreakdown_tenant_id" ON "stock_breakdowns" ("tenant_id");
-- Create index "stockbreakdown_tenant_id_child_item_id" to table: "stock_breakdowns"
CREATE INDEX "stockbreakdown_tenant_id_child_item_id" ON "stock_breakdowns" ("tenant_id", "child_item_id");
-- Create index "stockbreakdown_tenant_id_parent_item_id" to table: "stock_breakdowns"
CREATE INDEX "stockbreakdown_tenant_id_parent_item_id" ON "stock_breakdowns" ("tenant_id", "parent_item_id");
