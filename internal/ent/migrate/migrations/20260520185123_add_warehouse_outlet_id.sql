-- Modify "warehouses" table
ALTER TABLE "warehouses" ADD COLUMN "outlet_id" uuid NULL;
-- Create index "warehouse_tenant_id_outlet_id" to table: "warehouses"
CREATE INDEX "warehouse_tenant_id_outlet_id" ON "warehouses" ("tenant_id", "outlet_id");
