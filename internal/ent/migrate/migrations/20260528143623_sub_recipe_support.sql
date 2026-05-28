-- Modify "recipe_ingredients" table
ALTER TABLE "recipe_ingredients" ADD COLUMN "sub_recipe_id" uuid NULL, ADD CONSTRAINT "recipe_ingredients_recipes_used_as_ingredient" FOREIGN KEY ("sub_recipe_id") REFERENCES "recipes" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
