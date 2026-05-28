-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN "default_target_margin_percent" double precision NULL DEFAULT 30;
-- Modify "recipe_ingredients" table
ALTER TABLE "recipe_ingredients" ADD COLUMN "waste_percent" double precision NULL DEFAULT 0, ADD COLUMN "unit_id" uuid NULL, ADD CONSTRAINT "recipe_ingredients_units_recipe_ingredients" FOREIGN KEY ("unit_id") REFERENCES "units" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "cost_price" double precision NULL;
-- Modify "recipes" table
ALTER TABLE "recipes" ADD COLUMN "item_id" uuid NULL, ADD CONSTRAINT "recipes_items_produced_by_recipe" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "recipes_item_id_key" to table: "recipes"
CREATE UNIQUE INDEX "recipes_item_id_key" ON "recipes" ("item_id");
