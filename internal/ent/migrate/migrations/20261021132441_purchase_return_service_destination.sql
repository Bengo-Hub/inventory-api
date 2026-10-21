-- Modify "purchase_return_lines" table
ALTER TABLE "purchase_return_lines" ADD COLUMN "received_back_qty" double precision NOT NULL DEFAULT 0, ADD COLUMN "written_off_qty" double precision NOT NULL DEFAULT 0;
-- Modify "purchase_returns" table
ALTER TABLE "purchase_returns" ADD COLUMN "destination" character varying NOT NULL DEFAULT 'supplier', ADD COLUMN "service_status" character varying NULL, ADD COLUMN "expected_back_date" timestamptz NULL, ADD COLUMN "metadata" jsonb NULL;
-- Create index "purchasereturn_tenant_id_desti_f59c808130f3f02a73f33ffa8af9e5cf" to table: "purchase_returns"
CREATE INDEX "purchasereturn_tenant_id_desti_f59c808130f3f02a73f33ffa8af9e5cf" ON "purchase_returns" ("tenant_id", "destination", "service_status", "expected_back_date");
