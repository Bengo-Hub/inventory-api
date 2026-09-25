-- Modify "purchase_returns" table
ALTER TABLE "purchase_returns" ADD COLUMN "warehouse_id" uuid NULL;
-- Create index "purchasereturn_tenant_id_warehouse_id" to table: "purchase_returns"
CREATE INDEX "purchasereturn_tenant_id_warehouse_id" ON "purchase_returns" ("tenant_id", "warehouse_id");
-- Modify "purchase_return_lines" table
ALTER TABLE "purchase_return_lines" ALTER COLUMN "quantity" TYPE double precision;
