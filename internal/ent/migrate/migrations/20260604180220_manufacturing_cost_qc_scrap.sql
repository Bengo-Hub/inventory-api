-- Modify "batch_raw_materials" table
ALTER TABLE "batch_raw_materials" ADD COLUMN "cost" double precision NOT NULL DEFAULT 0;
-- Modify "production_batches" table
ALTER TABLE "production_batches" ADD COLUMN "scrap_quantity" double precision NOT NULL DEFAULT 0, ADD COLUMN "unit_cost" double precision NULL;
-- Modify "recipes" table
ALTER TABLE "recipes" ADD COLUMN "requires_qc" boolean NOT NULL DEFAULT false;
