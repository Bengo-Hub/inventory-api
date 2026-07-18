-- Modify "purchase_returns" table
ALTER TABLE "purchase_returns" ADD COLUMN "goods_receipt_id" uuid NULL;
-- Create index "purchasereturn_tenant_id_goods_receipt_id" to table: "purchase_returns"
CREATE INDEX "purchasereturn_tenant_id_goods_receipt_id" ON "purchase_returns" ("tenant_id", "goods_receipt_id");
