-- Modify "purchase_order_lines" table
ALTER TABLE "purchase_order_lines" ADD COLUMN "rebate_percent" double precision NOT NULL DEFAULT 0;
