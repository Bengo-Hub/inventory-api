-- Modify "purchase_order_lines" table
ALTER TABLE "purchase_order_lines" ADD COLUMN "tax_code" character varying NULL DEFAULT '', ADD COLUMN "tax_rate" double precision NOT NULL DEFAULT 0, ADD COLUMN "price_includes_tax" boolean NOT NULL DEFAULT false;
