-- Modify "goods_receipt_lines" table
ALTER TABLE "goods_receipt_lines" ALTER COLUMN "quantity_received" TYPE double precision, ALTER COLUMN "quantity_accepted" TYPE double precision, ALTER COLUMN "quantity_rejected" TYPE double precision;
-- Modify "inventory_balances" table
ALTER TABLE "inventory_balances" ALTER COLUMN "on_hand" TYPE double precision, ALTER COLUMN "available" TYPE double precision, ALTER COLUMN "reserved" TYPE double precision;
-- Modify "inventory_lots" table
ALTER TABLE "inventory_lots" ALTER COLUMN "quantity" TYPE double precision;
-- Modify "purchase_order_lines" table
ALTER TABLE "purchase_order_lines" ALTER COLUMN "quantity_ordered" TYPE double precision, ALTER COLUMN "quantity_received" TYPE double precision;
-- Modify "requisition_lines" table
ALTER TABLE "requisition_lines" ALTER COLUMN "quantity" TYPE double precision, ALTER COLUMN "approved_quantity" TYPE double precision;
-- Modify "rfq_awards" table
ALTER TABLE "rfq_awards" ALTER COLUMN "quantity" TYPE double precision;
-- Modify "rfq_lines" table
ALTER TABLE "rfq_lines" ALTER COLUMN "quantity" TYPE double precision;
-- Modify "stock_transfer_lines" table
ALTER TABLE "stock_transfer_lines" ALTER COLUMN "quantity" TYPE double precision;
