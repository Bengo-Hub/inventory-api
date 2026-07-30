-- Modify "inventory_lots" table
ALTER TABLE "inventory_lots" ADD COLUMN "is_cost_layer" boolean NOT NULL DEFAULT false, ADD COLUMN "received_at" timestamptz NULL, ADD COLUMN "goods_receipt_line_id" uuid NULL;
-- Backfill received_at for existing rows from created_at. Postgres sorts NULLs last in
-- ASC order, so without this a legacy lot would sort AFTER every newly-received layer and
-- break FIFO/LIFO consumption ordering.
UPDATE "inventory_lots" SET "received_at" = "created_at" WHERE "received_at" IS NULL;
-- Create index "inventorylot_goods_receipt_line_id" to table: "inventory_lots"
CREATE INDEX "inventorylot_goods_receipt_line_id" ON "inventory_lots" ("goods_receipt_line_id");
-- Create index "inventorylot_tenant_id_item_id_53ad209eba39043d22a7a3c58d3065af" to table: "inventory_lots"
CREATE INDEX "inventorylot_tenant_id_item_id_53ad209eba39043d22a7a3c58d3065af" ON "inventory_lots" ("tenant_id", "item_id", "warehouse_id", "status", "is_cost_layer");
