-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "purchase_price" double precision NULL, ADD COLUMN "purchase_pack_size" double precision NULL, ADD COLUMN "purchase_unit" character varying NULL, ADD COLUMN "yield_pct" double precision NULL DEFAULT 1;
-- Modify "recipes" table
ALTER TABLE "recipes" ADD COLUMN "selling_price" double precision NULL, ADD COLUMN "food_cost_pct" double precision NULL, ADD COLUMN "status" character varying NULL;
