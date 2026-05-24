-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "tax_code_id" character varying NULL, ADD COLUMN "tax_inclusive" boolean NOT NULL DEFAULT false;
