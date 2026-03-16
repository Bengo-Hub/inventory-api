-- Create "item_categories" table
CREATE TABLE "item_categories" ("id" uuid NOT NULL, "name" character varying NOT NULL, "description" text NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_categories_tenants_item_categories" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemcategory_tenant_id_name" to table: "item_categories"
CREATE INDEX "itemcategory_tenant_id_name" ON "item_categories" ("tenant_id", "name");
-- Modify "units" table
ALTER TABLE "units" DROP CONSTRAINT "units_items_units";
-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "unit_id" uuid NULL, ADD COLUMN "category_id" uuid NULL, ADD CONSTRAINT "items_item_categories_items" FOREIGN KEY ("category_id") REFERENCES "item_categories" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "items_units_units" FOREIGN KEY ("unit_id") REFERENCES "units" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "item_tenant_id_category_id" to table: "items"
CREATE INDEX "item_tenant_id_category_id" ON "items" ("tenant_id", "category_id");
-- Create "item_translations" table
CREATE TABLE "item_translations" ("id" uuid NOT NULL, "locale" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_translations_items_translations" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemtranslation_item_id_locale" to table: "item_translations"
CREATE UNIQUE INDEX "itemtranslation_item_id_locale" ON "item_translations" ("item_id", "locale");
-- Create "item_variants" table
CREATE TABLE "item_variants" ("id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "price" double precision NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_variants_items_variants" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemvariant_item_id_sku" to table: "item_variants"
CREATE UNIQUE INDEX "itemvariant_item_id_sku" ON "item_variants" ("item_id", "sku");
