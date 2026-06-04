-- Modify "assets" table
ALTER TABLE "assets" ADD COLUMN "item_id" uuid NULL;
-- Create "goods_receipts" table
CREATE TABLE "goods_receipts" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "grn_number" character varying NOT NULL, "purchase_order_id" uuid NOT NULL, "supplier_id" uuid NULL, "warehouse_id" uuid NULL, "received_by" uuid NULL, "received_date" timestamptz NOT NULL, "status" character varying NOT NULL DEFAULT 'draft', "notes" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "goodsreceipt_tenant_id_grn_number" to table: "goods_receipts"
CREATE UNIQUE INDEX "goodsreceipt_tenant_id_grn_number" ON "goods_receipts" ("tenant_id", "grn_number");
-- Create index "goodsreceipt_tenant_id_purchase_order_id" to table: "goods_receipts"
CREATE INDEX "goodsreceipt_tenant_id_purchase_order_id" ON "goods_receipts" ("tenant_id", "purchase_order_id");
-- Create index "goodsreceipt_tenant_id_status" to table: "goods_receipts"
CREATE INDEX "goodsreceipt_tenant_id_status" ON "goods_receipts" ("tenant_id", "status");
-- Create "goods_receipt_lines" table
CREATE TABLE "goods_receipt_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "purchase_order_line_id" uuid NULL, "item_id" uuid NOT NULL, "quantity_received" bigint NOT NULL DEFAULT 0, "quantity_accepted" bigint NOT NULL DEFAULT 0, "quantity_rejected" bigint NOT NULL DEFAULT 0, "unit_cost" double precision NOT NULL DEFAULT 0, "rejection_reason" text NULL, "created_at" timestamptz NOT NULL, "goods_receipt_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "goods_receipt_lines_goods_receipts_lines" FOREIGN KEY ("goods_receipt_id") REFERENCES "goods_receipts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "goodsreceiptline_item_id" to table: "goods_receipt_lines"
CREATE INDEX "goodsreceiptline_item_id" ON "goods_receipt_lines" ("item_id");
-- Create index "goodsreceiptline_tenant_id_goods_receipt_id" to table: "goods_receipt_lines"
CREATE INDEX "goodsreceiptline_tenant_id_goods_receipt_id" ON "goods_receipt_lines" ("tenant_id", "goods_receipt_id");
