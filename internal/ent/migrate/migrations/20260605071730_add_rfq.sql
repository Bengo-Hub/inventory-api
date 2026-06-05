-- Create "rf_qs" table
CREATE TABLE "rf_qs" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "rfq_number" character varying NOT NULL, "title" character varying NULL, "status" character varying NOT NULL DEFAULT 'draft', "requisition_id" uuid NULL, "warehouse_id" uuid NULL, "notes" text NULL, "due_date" timestamptz NULL, "created_by" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "rfq_tenant_id_rfq_number" to table: "rf_qs"
CREATE UNIQUE INDEX "rfq_tenant_id_rfq_number" ON "rf_qs" ("tenant_id", "rfq_number");
-- Create index "rfq_tenant_id_status" to table: "rf_qs"
CREATE INDEX "rfq_tenant_id_status" ON "rf_qs" ("tenant_id", "status");
-- Create "rfq_awards" table
CREATE TABLE "rfq_awards" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "rfq_line_id" uuid NOT NULL, "supplier_id" uuid NOT NULL, "unit_price" double precision NOT NULL DEFAULT 0, "quantity" bigint NOT NULL DEFAULT 1, "po_id" uuid NULL, "created_at" timestamptz NOT NULL, "rfq_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "rfq_awards_rf_qs_awards" FOREIGN KEY ("rfq_id") REFERENCES "rf_qs" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "rfqaward_rfq_id" to table: "rfq_awards"
CREATE INDEX "rfqaward_rfq_id" ON "rfq_awards" ("rfq_id");
-- Create index "rfqaward_rfq_id_rfq_line_id" to table: "rfq_awards"
CREATE UNIQUE INDEX "rfqaward_rfq_id_rfq_line_id" ON "rfq_awards" ("rfq_id", "rfq_line_id");
-- Create index "rfqaward_tenant_id" to table: "rfq_awards"
CREATE INDEX "rfqaward_tenant_id" ON "rfq_awards" ("tenant_id");
-- Create "rfq_lines" table
CREATE TABLE "rfq_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NULL, "description" character varying NULL, "quantity" bigint NOT NULL DEFAULT 1, "uom" character varying NULL, "rfq_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "rfq_lines_rf_qs_lines" FOREIGN KEY ("rfq_id") REFERENCES "rf_qs" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "rfqline_rfq_id" to table: "rfq_lines"
CREATE INDEX "rfqline_rfq_id" ON "rfq_lines" ("rfq_id");
-- Create index "rfqline_tenant_id" to table: "rfq_lines"
CREATE INDEX "rfqline_tenant_id" ON "rfq_lines" ("tenant_id");
-- Create "supplier_responses" table
CREATE TABLE "supplier_responses" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "supplier_id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'invited', "currency" character varying NOT NULL DEFAULT 'KES', "notes" text NULL, "submitted_at" timestamptz NULL, "quoted_items" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "rfq_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "supplier_responses_rf_qs_responses" FOREIGN KEY ("rfq_id") REFERENCES "rf_qs" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "supplierresponse_rfq_id_supplier_id" to table: "supplier_responses"
CREATE UNIQUE INDEX "supplierresponse_rfq_id_supplier_id" ON "supplier_responses" ("rfq_id", "supplier_id");
-- Create index "supplierresponse_tenant_id" to table: "supplier_responses"
CREATE INDEX "supplierresponse_tenant_id" ON "supplier_responses" ("tenant_id");
