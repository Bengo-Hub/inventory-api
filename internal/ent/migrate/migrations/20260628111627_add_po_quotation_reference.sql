-- Procure-to-order: link a draft purchase order back to the treasury sales quotation that
-- triggered it, and allow supplier/warehouse to be unset until assigned.
-- Modify "purchase_orders" table
ALTER TABLE "purchase_orders" DROP CONSTRAINT "purchase_orders_suppliers_purchase_orders", DROP CONSTRAINT "purchase_orders_warehouses_purchase_orders", ALTER COLUMN "supplier_id" DROP NOT NULL, ALTER COLUMN "warehouse_id" DROP NOT NULL, ADD COLUMN "quotation_id" uuid NULL, ADD COLUMN "quotation_number" character varying NULL, ADD CONSTRAINT "purchase_orders_suppliers_purchase_orders" FOREIGN KEY ("supplier_id") REFERENCES "suppliers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, ADD CONSTRAINT "purchase_orders_warehouses_purchase_orders" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "purchaseorder_tenant_id_quotation_id" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_quotation_id" ON "purchase_orders" ("tenant_id", "quotation_id");
