-- Modify "contracts" table
ALTER TABLE "contracts" ADD COLUMN "rfq_id" uuid NULL;
-- Modify "purchase_orders" table
ALTER TABLE "purchase_orders" ADD COLUMN "requisition_id" uuid NULL, ADD COLUMN "rfq_id" uuid NULL, ADD COLUMN "pay_term_days" bigint NULL, ADD COLUMN "additional_shipping_charges" double precision NOT NULL DEFAULT 0;
-- Modify "service_deliveries" table
ALTER TABLE "service_deliveries" ADD COLUMN "amount" double precision NOT NULL DEFAULT 0, ADD COLUMN "currency" character varying NOT NULL DEFAULT 'KES';
