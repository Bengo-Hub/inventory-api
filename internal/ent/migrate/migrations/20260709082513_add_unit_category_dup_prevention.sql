-- Drop index "itemcategory_tenant_id_name" from table: "item_categories"
DROP INDEX "itemcategory_tenant_id_name";
-- Create index "itemcategory_tenant_id_name" to table: "item_categories"
CREATE UNIQUE INDEX "itemcategory_tenant_id_name" ON "item_categories" ("tenant_id", "name");
-- Create index "unit_abbreviation" to table: "units"
CREATE UNIQUE INDEX "unit_abbreviation" ON "units" ("abbreviation");
