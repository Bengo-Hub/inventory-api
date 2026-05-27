-- Create index "item_tenant_id_created_at" to table: "items"
CREATE INDEX "item_tenant_id_created_at" ON "items" ("tenant_id", "created_at");
-- Create index "item_tenant_id_unit_id" to table: "items"
CREATE INDEX "item_tenant_id_unit_id" ON "items" ("tenant_id", "unit_id");
