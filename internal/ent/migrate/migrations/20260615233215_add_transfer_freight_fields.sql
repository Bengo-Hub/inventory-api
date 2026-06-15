-- Modify "stock_transfers" table
ALTER TABLE "stock_transfers" ADD COLUMN "reference_no" character varying NULL, ADD COLUMN "shipping_charges" double precision NOT NULL DEFAULT 0, ADD COLUMN "carrier" character varying NULL, ADD COLUMN "freight_notes" text NULL;
