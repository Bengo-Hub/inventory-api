-- Modify "purchase_returns" table: make the goods_receipt_id lookup index unique, enforcing "one
-- auto return per GRN" at the DB level (closes the auto-create's Exist()-then-Create() race).
-- Postgres unique indexes treat every NULL as distinct, so manual returns (goods_receipt_id NULL)
-- are unaffected.
DROP INDEX "purchasereturn_tenant_id_goods_receipt_id";
CREATE UNIQUE INDEX "purchasereturn_tenant_id_goods_receipt_id" ON "purchase_returns" ("tenant_id", "goods_receipt_id");
