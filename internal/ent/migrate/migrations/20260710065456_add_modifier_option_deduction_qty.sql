-- Modify "modifier_options" table
ALTER TABLE "modifier_options" ADD COLUMN "deduction_qty" double precision NOT NULL DEFAULT 1, ADD COLUMN "deduction_unit" character varying NULL;
