-- Create "item_brands" table
CREATE TABLE "item_brands" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "description" text NULL, "logo_url" character varying NULL, "sort_order" bigint NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "itembrand_tenant_id_code" to table: "item_brands"
CREATE UNIQUE INDEX "itembrand_tenant_id_code" ON "item_brands" ("tenant_id", "code");
-- Create index "itembrand_tenant_id_name" to table: "item_brands"
CREATE INDEX "itembrand_tenant_id_name" ON "item_brands" ("tenant_id", "name");
-- Create index "itembrand_tenant_id_sort_order" to table: "item_brands"
CREATE INDEX "itembrand_tenant_id_sort_order" ON "item_brands" ("tenant_id", "sort_order");
-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "brand_id" uuid NULL, ADD CONSTRAINT "items_item_brands_items" FOREIGN KEY ("brand_id") REFERENCES "item_brands" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "item_tenant_id_brand_id" to table: "items"
CREATE INDEX "item_tenant_id_brand_id" ON "items" ("tenant_id", "brand_id");
