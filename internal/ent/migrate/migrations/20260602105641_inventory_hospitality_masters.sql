-- Modify "bundle_components" table
ALTER TABLE "bundle_components" ADD COLUMN "component_kind" character varying NOT NULL DEFAULT 'ITEM', ADD COLUMN "meal_period" character varying NULL, ADD COLUMN "is_metered" boolean NOT NULL DEFAULT false, ADD COLUMN "unit" character varying NULL;
-- Modify "bundles" table
ALTER TABLE "bundles" ADD COLUMN "package_type" character varying NOT NULL DEFAULT 'RETAIL_KIT', ADD COLUMN "price_basis" character varying NOT NULL DEFAULT 'flat', ADD COLUMN "min_delegates" bigint NULL, ADD COLUMN "accommodation_included" boolean NOT NULL DEFAULT false, ADD COLUMN "sessions_total" bigint NULL, ADD COLUMN "validity_days" bigint NULL;
-- Modify "item_pricings" table
ALTER TABLE "item_pricings" ADD COLUMN "outlet_id" uuid NULL, ADD COLUMN "tier_basis" character varying NOT NULL DEFAULT 'default';
-- Drop superseded 3-column unique index (replaced by the outlet_id-aware unique index below so per-outlet rate overrides are allowed)
DROP INDEX IF EXISTS "itempricing_tenant_id_item_id_pricing_tier_id";
-- Create index "itempricing_outlet_id" to table: "item_pricings"
CREATE INDEX "itempricing_outlet_id" ON "item_pricings" ("outlet_id");
-- Create index "itempricing_tenant_id_item_id_pricing_tier_id_outlet_id" to table: "item_pricings"
CREATE UNIQUE INDEX "itempricing_tenant_id_item_id_pricing_tier_id_outlet_id" ON "item_pricings" ("tenant_id", "item_id", "pricing_tier_id", "outlet_id");
-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "use_case" character varying NOT NULL DEFAULT 'RETAIL', ADD COLUMN "meal_plan" character varying NULL, ADD COLUMN "occupancy_basis" character varying NULL, ADD COLUMN "max_adults" bigint NULL, ADD COLUMN "max_children" bigint NULL, ADD COLUMN "extra_bed_allowed" boolean NOT NULL DEFAULT false, ADD COLUMN "single_supplement" double precision NULL;
-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "enable_room_pricing" boolean NOT NULL DEFAULT false, ADD COLUMN "enable_facility_booking" boolean NOT NULL DEFAULT false, ADD COLUMN "enable_conference_packages" boolean NOT NULL DEFAULT false;
