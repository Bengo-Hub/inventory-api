-- Create "assets" table
CREATE TABLE "assets" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_tag" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "category_id" uuid NULL, "serial_number" character varying NULL, "model" character varying NULL, "manufacturer" character varying NULL, "barcode" character varying NULL, "purchase_date" timestamptz NULL, "purchase_cost" double precision NOT NULL DEFAULT 0, "current_value" double precision NOT NULL DEFAULT 0, "salvage_value" double precision NOT NULL DEFAULT 0, "depreciation_rate" double precision NOT NULL DEFAULT 0, "depreciation_method" character varying NOT NULL DEFAULT 'straight_line', "accumulated_depreciation" double precision NOT NULL DEFAULT 0, "book_value" double precision NOT NULL DEFAULT 0, "location" character varying NULL, "outlet_id" uuid NULL, "assigned_to" uuid NULL, "custodian_id" uuid NULL, "status" character varying NOT NULL DEFAULT 'active', "condition" character varying NULL, "warranty_expiry" timestamptz NULL, "last_maintenance" timestamptz NULL, "next_maintenance" timestamptz NULL, "maintenance_schedule" character varying NULL, "notes" text NULL, "is_active" boolean NOT NULL DEFAULT true, "created_by" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "asset_barcode" to table: "assets"
CREATE INDEX "asset_barcode" ON "assets" ("barcode");
-- Create index "asset_serial_number" to table: "assets"
CREATE INDEX "asset_serial_number" ON "assets" ("serial_number");
-- Create index "asset_tenant_id_asset_tag" to table: "assets"
CREATE UNIQUE INDEX "asset_tenant_id_asset_tag" ON "assets" ("tenant_id", "asset_tag");
-- Create index "asset_tenant_id_category_id" to table: "assets"
CREATE INDEX "asset_tenant_id_category_id" ON "assets" ("tenant_id", "category_id");
-- Create index "asset_tenant_id_status" to table: "assets"
CREATE INDEX "asset_tenant_id_status" ON "assets" ("tenant_id", "status");
-- Create "asset_audits" table
CREATE TABLE "asset_audits" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "audit_date" timestamptz NOT NULL, "auditor_id" uuid NULL, "status" character varying NOT NULL DEFAULT 'planned', "location_verified" character varying NULL, "condition_verified" character varying NULL, "custodian_verified" uuid NULL, "discrepancies" text NULL, "recommendations" text NULL, "next_audit_date" timestamptz NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetaudit_tenant_id_asset_id" to table: "asset_audits"
CREATE INDEX "assetaudit_tenant_id_asset_id" ON "asset_audits" ("tenant_id", "asset_id");
-- Create index "assetaudit_tenant_id_status" to table: "asset_audits"
CREATE INDEX "assetaudit_tenant_id_status" ON "asset_audits" ("tenant_id", "status");
-- Create "asset_categories" table
CREATE TABLE "asset_categories" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "description" text NULL, "parent_id" uuid NULL, "depreciation_rate" double precision NOT NULL DEFAULT 0, "useful_life_years" bigint NOT NULL DEFAULT 5, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetcategory_tenant_id_is_active" to table: "asset_categories"
CREATE INDEX "assetcategory_tenant_id_is_active" ON "asset_categories" ("tenant_id", "is_active");
-- Create index "assetcategory_tenant_id_name" to table: "asset_categories"
CREATE UNIQUE INDEX "assetcategory_tenant_id_name" ON "asset_categories" ("tenant_id", "name");
-- Create "asset_disposals" table
CREATE TABLE "asset_disposals" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "disposal_date" timestamptz NOT NULL, "disposal_method" character varying NOT NULL DEFAULT 'sold', "disposal_value" double precision NOT NULL DEFAULT 0, "reason" text NULL, "approved_by" uuid NULL, "status" character varying NOT NULL DEFAULT 'pending', "notes" text NULL, "disposal_certificate" character varying NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetdisposal_tenant_id_asset_id" to table: "asset_disposals"
CREATE INDEX "assetdisposal_tenant_id_asset_id" ON "asset_disposals" ("tenant_id", "asset_id");
-- Create index "assetdisposal_tenant_id_status" to table: "asset_disposals"
CREATE INDEX "assetdisposal_tenant_id_status" ON "asset_disposals" ("tenant_id", "status");
-- Create "asset_insurances" table
CREATE TABLE "asset_insurances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "policy_number" character varying NOT NULL, "provider" character varying NULL, "policy_type" character varying NULL, "coverage_amount" double precision NOT NULL DEFAULT 0, "premium_amount" double precision NOT NULL DEFAULT 0, "start_date" timestamptz NOT NULL, "end_date" timestamptz NOT NULL, "deductible" double precision NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "notes" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetinsurance_tenant_id_asset_id" to table: "asset_insurances"
CREATE INDEX "assetinsurance_tenant_id_asset_id" ON "asset_insurances" ("tenant_id", "asset_id");
-- Create index "assetinsurance_tenant_id_policy_number" to table: "asset_insurances"
CREATE UNIQUE INDEX "assetinsurance_tenant_id_policy_number" ON "asset_insurances" ("tenant_id", "policy_number");
-- Create "asset_maintenances" table
CREATE TABLE "asset_maintenances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "maintenance_type" character varying NOT NULL DEFAULT 'preventive', "scheduled_date" timestamptz NOT NULL, "completed_date" timestamptz NULL, "performed_by" character varying NULL, "cost" double precision NOT NULL DEFAULT 0, "description" text NULL, "findings" text NULL, "recommendations" text NULL, "next_maintenance_date" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'scheduled', "priority" character varying NOT NULL DEFAULT 'medium', "downtime_hours" double precision NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetmaintenance_tenant_id_asset_id" to table: "asset_maintenances"
CREATE INDEX "assetmaintenance_tenant_id_asset_id" ON "asset_maintenances" ("tenant_id", "asset_id");
-- Create index "assetmaintenance_tenant_id_status" to table: "asset_maintenances"
CREATE INDEX "assetmaintenance_tenant_id_status" ON "asset_maintenances" ("tenant_id", "status");
-- Create "asset_reservations" table
CREATE TABLE "asset_reservations" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "reserved_by" uuid NOT NULL, "start_date" timestamptz NOT NULL, "end_date" timestamptz NOT NULL, "purpose" text NULL, "status" character varying NOT NULL DEFAULT 'pending', "approved_by" uuid NULL, "notes" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assetreservation_tenant_id_asset_id" to table: "asset_reservations"
CREATE INDEX "assetreservation_tenant_id_asset_id" ON "asset_reservations" ("tenant_id", "asset_id");
-- Create index "assetreservation_tenant_id_status" to table: "asset_reservations"
CREATE INDEX "assetreservation_tenant_id_status" ON "asset_reservations" ("tenant_id", "status");
-- Create "asset_transfers" table
CREATE TABLE "asset_transfers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "asset_id" uuid NOT NULL, "from_location" character varying NULL, "to_location" character varying NULL, "from_user" uuid NULL, "to_user" uuid NULL, "transfer_date" timestamptz NOT NULL, "scheduled_date" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'pending', "reason" text NULL, "transferred_by" uuid NULL, "approved_by" uuid NULL, "notes" text NULL, "tracking_number" character varying NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "assettransfer_tenant_id_asset_id" to table: "asset_transfers"
CREATE INDEX "assettransfer_tenant_id_asset_id" ON "asset_transfers" ("tenant_id", "asset_id");
-- Create index "assettransfer_tenant_id_status" to table: "asset_transfers"
CREATE INDEX "assettransfer_tenant_id_status" ON "asset_transfers" ("tenant_id", "status");
