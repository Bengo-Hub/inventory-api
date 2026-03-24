-- Create "stock_adjustments" table
CREATE TABLE "stock_adjustments" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "quantity_before" double precision NOT NULL, "quantity_change" double precision NOT NULL, "quantity_after" double precision NOT NULL, "reason" character varying NOT NULL, "reference" character varying NULL, "notes" text NULL, "adjusted_by" uuid NOT NULL, "adjusted_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockadjustment_adjusted_at" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_adjusted_at" ON "stock_adjustments" ("adjusted_at");
-- Create index "stockadjustment_tenant_id_item_id" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_item_id" ON "stock_adjustments" ("tenant_id", "item_id");
-- Create index "stockadjustment_tenant_id_reason" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_reason" ON "stock_adjustments" ("tenant_id", "reason");
-- Create index "stockadjustment_tenant_id_warehouse_id" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_warehouse_id" ON "stock_adjustments" ("tenant_id", "warehouse_id");
