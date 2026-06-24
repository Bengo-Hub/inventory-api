-- Add pricing guardrail + goods margin columns to "items"
ALTER TABLE "items" ADD COLUMN "min_selling_price" double precision NULL, ADD COLUMN "max_selling_price" double precision NULL, ADD COLUMN "target_margin_percent" double precision NULL;
-- Add serial capture column to "goods_receipt_lines"
ALTER TABLE "goods_receipt_lines" ADD COLUMN "serials" jsonb NULL;
-- Create "inventory_serials" table
CREATE TABLE "inventory_serials" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NULL, "serial_number" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'available', "received_at" timestamptz NOT NULL, "goods_receipt_line_id" uuid NULL, "sold_at" timestamptz NULL, "pos_order_line_id" character varying NULL, "notes" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "inventoryserial_tenant_id_item_id_serial_number" to table: "inventory_serials"
CREATE UNIQUE INDEX "inventoryserial_tenant_id_item_id_serial_number" ON "inventory_serials" ("tenant_id", "item_id", "serial_number");
-- Create index "inventoryserial_tenant_id_item_id_status" to table: "inventory_serials"
CREATE INDEX "inventoryserial_tenant_id_item_id_status" ON "inventory_serials" ("tenant_id", "item_id", "status");
-- Create index "inventoryserial_tenant_id_status" to table: "inventory_serials"
CREATE INDEX "inventoryserial_tenant_id_status" ON "inventory_serials" ("tenant_id", "status");
