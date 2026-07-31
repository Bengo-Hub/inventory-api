-- Modify "purchase_order_lines" table
ALTER TABLE "purchase_order_lines" ADD COLUMN "new_selling_price" double precision NULL, ADD COLUMN "price_scope" character varying NULL DEFAULT 'all_stock';
