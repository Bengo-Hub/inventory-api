-- Modify "item_categories" table
ALTER TABLE "item_categories" ADD COLUMN "is_global" boolean NOT NULL DEFAULT false;
