-- Create index "inventorybalance_tenant_id_warehouse_id" to table: "inventory_balances"
CREATE INDEX "inventorybalance_tenant_id_warehouse_id" ON "inventory_balances" ("tenant_id", "warehouse_id");
-- Create index "itemvariant_barcode_trgm" to table: "item_variants"
CREATE INDEX "itemvariant_barcode_trgm" ON "item_variants" USING gin ("barcode" gin_trgm_ops);
-- Create index "item_barcode" to table: "items"
CREATE INDEX "item_barcode" ON "items" USING gin ("barcode" gin_trgm_ops);
-- Create index "item_gtin" to table: "items"
CREATE INDEX "item_gtin" ON "items" USING gin ("gtin" gin_trgm_ops);
-- Create index "item_name" to table: "items"
CREATE INDEX "item_name" ON "items" USING gin ("name" gin_trgm_ops);
-- Create index "item_sku" to table: "items"
CREATE INDEX "item_sku" ON "items" USING gin ("sku" gin_trgm_ops);
