-- Modify "stock_transfers" table
ALTER TABLE "stock_transfers" ADD COLUMN "origin" character varying NOT NULL DEFAULT 'manual';
