-- Create "consumption_lines" table
CREATE TABLE "consumption_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "consumption_id" uuid NOT NULL, "order_id" uuid NOT NULL, "warehouse_id" uuid NULL, "outlet_id" uuid NULL, "recipe_id" uuid NULL, "recipe_sku" character varying NULL, "finished_item_sku" character varying NOT NULL, "ingredient_item_id" uuid NOT NULL, "ingredient_sku" character varying NOT NULL, "quantity" double precision NOT NULL, "unit" character varying NULL, "unit_cost" double precision NOT NULL DEFAULT 0, "total_cost" double precision NOT NULL DEFAULT 0, "theoretical" boolean NOT NULL DEFAULT false, "reason" character varying NOT NULL DEFAULT 'sale', "consumed_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "consumptionline_consumption_id" to table: "consumption_lines"
CREATE INDEX "consumptionline_consumption_id" ON "consumption_lines" ("consumption_id");
-- Create index "consumptionline_tenant_id_ingredient_item_id_consumed_at" to table: "consumption_lines"
CREATE INDEX "consumptionline_tenant_id_ingredient_item_id_consumed_at" ON "consumption_lines" ("tenant_id", "ingredient_item_id", "consumed_at");
-- Create index "consumptionline_tenant_id_order_id" to table: "consumption_lines"
CREATE INDEX "consumptionline_tenant_id_order_id" ON "consumption_lines" ("tenant_id", "order_id");
-- Create index "consumptionline_tenant_id_recipe_id_consumed_at" to table: "consumption_lines"
CREATE INDEX "consumptionline_tenant_id_recipe_id_consumed_at" ON "consumption_lines" ("tenant_id", "recipe_id", "consumed_at");
-- Create "item_consumption_dailies" table
CREATE TABLE "item_consumption_dailies" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "outlet_id" uuid NULL, "item_id" uuid NOT NULL, "item_sku" character varying NULL, "recipe_id" uuid NOT NULL, "recipe_sku" character varying NULL, "bucket_date" timestamptz NOT NULL, "quantity" double precision NOT NULL DEFAULT 0, "total_cost" double precision NOT NULL DEFAULT 0, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "itemconsumptiondaily_tenant_id_3d8e7a2535faa9582aff8eedf60a7ff1" to table: "item_consumption_dailies"
CREATE UNIQUE INDEX "itemconsumptiondaily_tenant_id_3d8e7a2535faa9582aff8eedf60a7ff1" ON "item_consumption_dailies" ("tenant_id", "warehouse_id", "item_id", "recipe_id", "bucket_date");
-- Create index "itemconsumptiondaily_tenant_id_item_id_bucket_date" to table: "item_consumption_dailies"
CREATE INDEX "itemconsumptiondaily_tenant_id_item_id_bucket_date" ON "item_consumption_dailies" ("tenant_id", "item_id", "bucket_date");
-- Create "stock_level_events" table
CREATE TABLE "stock_level_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "outlet_id" uuid NULL, "event_type" character varying NOT NULL, "on_hand_at_event" double precision NOT NULL DEFAULT 0, "reorder_level_at_event" double precision NOT NULL DEFAULT 0, "occurred_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stocklevelevent_tenant_id_item_id_warehouse_id_occurred_at" to table: "stock_level_events"
CREATE INDEX "stocklevelevent_tenant_id_item_id_warehouse_id_occurred_at" ON "stock_level_events" ("tenant_id", "item_id", "warehouse_id", "occurred_at");
