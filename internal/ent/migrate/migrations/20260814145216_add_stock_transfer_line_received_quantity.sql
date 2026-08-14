-- Modify "stock_transfer_lines" table
ALTER TABLE "stock_transfer_lines" ADD COLUMN "received_quantity" double precision NULL, ADD COLUMN "variance_reason" character varying NULL;
