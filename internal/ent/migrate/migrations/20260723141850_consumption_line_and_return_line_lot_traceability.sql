-- Modify "consumption_lines" table
ALTER TABLE "consumption_lines" ADD COLUMN "lot_id" uuid NULL, ADD COLUMN "lot_number" character varying NULL, ADD COLUMN "expiry_date" timestamptz NULL;
-- Create index "consumptionline_tenant_id_lot_id" to table: "consumption_lines"
CREATE INDEX "consumptionline_tenant_id_lot_id" ON "consumption_lines" ("tenant_id", "lot_id");
-- Modify "purchase_return_lines" table
ALTER TABLE "purchase_return_lines" ADD COLUMN "lot_id" uuid NULL;
