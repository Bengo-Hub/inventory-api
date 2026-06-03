-- Create "production_batches" table
CREATE TABLE "production_batches" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "outlet_id" uuid NULL, "batch_number" character varying NOT NULL, "recipe_id" uuid NOT NULL, "scheduled_date" timestamptz NOT NULL, "start_date" timestamptz NULL, "end_date" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'planned', "planned_quantity" double precision NOT NULL DEFAULT 0, "actual_quantity" double precision NULL, "labor_cost" double precision NOT NULL DEFAULT 0, "overhead_cost" double precision NOT NULL DEFAULT 0, "notes" text NULL, "created_by" uuid NULL, "supervisor_id" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "productionbatch_recipe_id" to table: "production_batches"
CREATE INDEX "productionbatch_recipe_id" ON "production_batches" ("recipe_id");
-- Create index "productionbatch_tenant_id_batch_number" to table: "production_batches"
CREATE UNIQUE INDEX "productionbatch_tenant_id_batch_number" ON "production_batches" ("tenant_id", "batch_number");
-- Create index "productionbatch_tenant_id_status" to table: "production_batches"
CREATE INDEX "productionbatch_tenant_id_status" ON "production_batches" ("tenant_id", "status");
-- Create "service_deliveries" table
CREATE TABLE "service_deliveries" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "requisition_line_id" uuid NULL, "provider_id" uuid NULL, "start_date" timestamptz NOT NULL, "end_date" timestamptz NOT NULL, "deliverables" text NULL, "status" character varying NOT NULL DEFAULT 'scheduled', "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "servicedelivery_requisition_line_id" to table: "service_deliveries"
CREATE INDEX "servicedelivery_requisition_line_id" ON "service_deliveries" ("requisition_line_id");
-- Create index "servicedelivery_tenant_id_status" to table: "service_deliveries"
CREATE INDEX "servicedelivery_tenant_id_status" ON "service_deliveries" ("tenant_id", "status");
-- Create "supplier_performances" table
CREATE TABLE "supplier_performances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "supplier_id" uuid NOT NULL, "period_start" timestamptz NOT NULL, "period_end" timestamptz NOT NULL, "on_time_delivery_rate" double precision NOT NULL DEFAULT 0, "defect_rate" double precision NOT NULL DEFAULT 0, "average_lead_time_days" double precision NOT NULL DEFAULT 0, "total_spend" double precision NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "supplierperformance_supplier_id_period_start_period_end" to table: "supplier_performances"
CREATE INDEX "supplierperformance_supplier_id_period_start_period_end" ON "supplier_performances" ("supplier_id", "period_start", "period_end");
-- Create index "supplierperformance_tenant_id_supplier_id" to table: "supplier_performances"
CREATE INDEX "supplierperformance_tenant_id_supplier_id" ON "supplier_performances" ("tenant_id", "supplier_id");
-- Create "batch_raw_materials" table
CREATE TABLE "batch_raw_materials" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "unit_id" uuid NULL, "quantity" double precision NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "production_batch_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "batch_raw_materials_production_batches_raw_materials" FOREIGN KEY ("production_batch_id") REFERENCES "production_batches" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "batchrawmaterial_item_id" to table: "batch_raw_materials"
CREATE INDEX "batchrawmaterial_item_id" ON "batch_raw_materials" ("item_id");
-- Create index "batchrawmaterial_tenant_id_production_batch_id" to table: "batch_raw_materials"
CREATE INDEX "batchrawmaterial_tenant_id_production_batch_id" ON "batch_raw_materials" ("tenant_id", "production_batch_id");
-- Create "contracts" table
CREATE TABLE "contracts" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "supplier_id" uuid NOT NULL, "title" character varying NOT NULL, "start_date" timestamptz NOT NULL, "end_date" timestamptz NOT NULL, "value" double precision NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'draft', "terms" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "contract_tenant_id_status" to table: "contracts"
CREATE INDEX "contract_tenant_id_status" ON "contracts" ("tenant_id", "status");
-- Create index "contract_tenant_id_supplier_id" to table: "contracts"
CREATE INDEX "contract_tenant_id_supplier_id" ON "contracts" ("tenant_id", "supplier_id");
-- Create "contract_order_links" table
CREATE TABLE "contract_order_links" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "purchase_order_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "contract_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "contract_order_links_contracts_order_links" FOREIGN KEY ("contract_id") REFERENCES "contracts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "contractorderlink_contract_id_purchase_order_id" to table: "contract_order_links"
CREATE UNIQUE INDEX "contractorderlink_contract_id_purchase_order_id" ON "contract_order_links" ("contract_id", "purchase_order_id");
-- Create "purchase_returns" table
CREATE TABLE "purchase_returns" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "return_number" character varying NULL, "purchase_order_id" uuid NULL, "supplier_id" uuid NULL, "added_by" uuid NULL, "reason" text NULL, "return_amount" double precision NOT NULL DEFAULT 0, "return_amount_due" double precision NOT NULL DEFAULT 0, "payment_status" character varying NOT NULL DEFAULT 'pending', "date_returned" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "purchasereturn_tenant_id_payment_status" to table: "purchase_returns"
CREATE INDEX "purchasereturn_tenant_id_payment_status" ON "purchase_returns" ("tenant_id", "payment_status");
-- Create index "purchasereturn_tenant_id_purchase_order_id" to table: "purchase_returns"
CREATE INDEX "purchasereturn_tenant_id_purchase_order_id" ON "purchase_returns" ("tenant_id", "purchase_order_id");
-- Create "purchase_return_lines" table
CREATE TABLE "purchase_return_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "quantity" bigint NOT NULL DEFAULT 1, "sub_total" double precision NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "purchase_return_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "purchase_return_lines_purchase_returns_lines" FOREIGN KEY ("purchase_return_id") REFERENCES "purchase_returns" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "purchasereturnline_item_id" to table: "purchase_return_lines"
CREATE INDEX "purchasereturnline_item_id" ON "purchase_return_lines" ("item_id");
-- Create index "purchasereturnline_tenant_id_purchase_return_id" to table: "purchase_return_lines"
CREATE INDEX "purchasereturnline_tenant_id_purchase_return_id" ON "purchase_return_lines" ("tenant_id", "purchase_return_id");
-- Create "quality_checks" table
CREATE TABLE "quality_checks" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "inspector_id" uuid NULL, "check_date" timestamptz NOT NULL, "result" character varying NOT NULL DEFAULT 'pending', "notes" text NULL, "created_at" timestamptz NOT NULL, "production_batch_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "quality_checks_production_batches_quality_checks" FOREIGN KEY ("production_batch_id") REFERENCES "production_batches" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "qualitycheck_result" to table: "quality_checks"
CREATE INDEX "qualitycheck_result" ON "quality_checks" ("result");
-- Create index "qualitycheck_tenant_id_production_batch_id" to table: "quality_checks"
CREATE INDEX "qualitycheck_tenant_id_production_batch_id" ON "quality_checks" ("tenant_id", "production_batch_id");
-- Create "requisitions" table
CREATE TABLE "requisitions" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "outlet_id" uuid NULL, "reference_number" character varying NOT NULL, "requester_id" uuid NULL, "request_type" character varying NOT NULL DEFAULT 'inventory', "purpose" text NULL, "priority" character varying NOT NULL DEFAULT 'medium', "required_by_date" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'draft', "notes" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "requisition_tenant_id_priority" to table: "requisitions"
CREATE INDEX "requisition_tenant_id_priority" ON "requisitions" ("tenant_id", "priority");
-- Create index "requisition_tenant_id_reference_number" to table: "requisitions"
CREATE UNIQUE INDEX "requisition_tenant_id_reference_number" ON "requisitions" ("tenant_id", "reference_number");
-- Create index "requisition_tenant_id_request_type" to table: "requisitions"
CREATE INDEX "requisition_tenant_id_request_type" ON "requisitions" ("tenant_id", "request_type");
-- Create index "requisition_tenant_id_status" to table: "requisitions"
CREATE INDEX "requisition_tenant_id_status" ON "requisitions" ("tenant_id", "status");
-- Create "requisition_lines" table
CREATE TABLE "requisition_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_type" character varying NOT NULL DEFAULT 'inventory', "item_id" uuid NULL, "quantity" bigint NOT NULL DEFAULT 1, "approved_quantity" bigint NULL, "urgent" boolean NOT NULL DEFAULT false, "description" text NULL, "specifications" text NULL, "estimated_price" double precision NULL, "supplier_id" uuid NULL, "service_description" text NULL, "expected_deliverables" text NULL, "duration" character varying NULL, "start_date" timestamptz NULL, "end_date" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "requisition_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "requisition_lines_requisitions_lines" FOREIGN KEY ("requisition_id") REFERENCES "requisitions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "requisitionline_item_id" to table: "requisition_lines"
CREATE INDEX "requisitionline_item_id" ON "requisition_lines" ("item_id");
-- Create index "requisitionline_item_type" to table: "requisition_lines"
CREATE INDEX "requisitionline_item_type" ON "requisition_lines" ("item_type");
-- Create index "requisitionline_tenant_id_requisition_id" to table: "requisition_lines"
CREATE INDEX "requisitionline_tenant_id_requisition_id" ON "requisition_lines" ("tenant_id", "requisition_id");
