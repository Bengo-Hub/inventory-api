-- Drop index "warehouse_tenant_id_outlet_id" from table: "warehouses"
DROP INDEX "warehouse_tenant_id_outlet_id";
-- Create index "warehouse_tenant_id_outlet_id" to table: "warehouses"
CREATE UNIQUE INDEX "warehouse_tenant_id_outlet_id" ON "warehouses" ("tenant_id", "outlet_id");
