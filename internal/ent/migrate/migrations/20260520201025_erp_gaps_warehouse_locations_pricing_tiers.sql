-- Create "item_pricings" table
CREATE TABLE "item_pricings" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "pricing_tier_id" uuid NOT NULL, "price" double precision NOT NULL DEFAULT 0, "currency" character varying NOT NULL DEFAULT 'KES', "effective_from" timestamptz NOT NULL, "effective_to" timestamptz NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "itempricing_is_active" to table: "item_pricings"
CREATE INDEX "itempricing_is_active" ON "item_pricings" ("is_active");
-- Create index "itempricing_item_id" to table: "item_pricings"
CREATE INDEX "itempricing_item_id" ON "item_pricings" ("item_id");
-- Create index "itempricing_pricing_tier_id" to table: "item_pricings"
CREATE INDEX "itempricing_pricing_tier_id" ON "item_pricings" ("pricing_tier_id");
-- Create index "itempricing_tenant_id_item_id_pricing_tier_id" to table: "item_pricings"
CREATE UNIQUE INDEX "itempricing_tenant_id_item_id_pricing_tier_id" ON "item_pricings" ("tenant_id", "item_id", "pricing_tier_id");
-- Create "pricing_tiers" table
CREATE TABLE "pricing_tiers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "description" character varying NULL, "is_default" boolean NOT NULL DEFAULT false, "is_active" boolean NOT NULL DEFAULT true, "sort_order" bigint NOT NULL DEFAULT 0, PRIMARY KEY ("id"));
-- Create index "pricingtier_tenant_id" to table: "pricing_tiers"
CREATE INDEX "pricingtier_tenant_id" ON "pricing_tiers" ("tenant_id");
-- Create index "pricingtier_tenant_id_code" to table: "pricing_tiers"
CREATE UNIQUE INDEX "pricingtier_tenant_id_code" ON "pricing_tiers" ("tenant_id", "code");
-- Create "warehouse_locations" table
CREATE TABLE "warehouse_locations" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "parent_id" uuid NULL, "code" character varying NOT NULL, "name" character varying NOT NULL, "type" character varying NOT NULL DEFAULT 'bin', "depth" bigint NOT NULL DEFAULT 0, "path" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "capacity_units" bigint NULL, "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "warehouselocation_parent_id" to table: "warehouse_locations"
CREATE INDEX "warehouselocation_parent_id" ON "warehouse_locations" ("parent_id");
-- Create index "warehouselocation_tenant_id_warehouse_id_code" to table: "warehouse_locations"
CREATE UNIQUE INDEX "warehouselocation_tenant_id_warehouse_id_code" ON "warehouse_locations" ("tenant_id", "warehouse_id", "code");
-- Create index "warehouselocation_type" to table: "warehouse_locations"
CREATE INDEX "warehouselocation_type" ON "warehouse_locations" ("type");
-- Create index "warehouselocation_warehouse_id" to table: "warehouse_locations"
CREATE INDEX "warehouselocation_warehouse_id" ON "warehouse_locations" ("warehouse_id");
