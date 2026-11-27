-- Modify "goods_receipt_lines" table
ALTER TABLE "goods_receipt_lines" ADD COLUMN "close_remaining" boolean NOT NULL DEFAULT false;
-- Modify "purchase_order_lines" table
ALTER TABLE "purchase_order_lines" ADD COLUMN "quantity_closed" double precision NOT NULL DEFAULT 0;
