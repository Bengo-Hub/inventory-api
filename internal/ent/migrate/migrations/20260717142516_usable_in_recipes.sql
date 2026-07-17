-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "usable_in_recipes" boolean NOT NULL DEFAULT false;
