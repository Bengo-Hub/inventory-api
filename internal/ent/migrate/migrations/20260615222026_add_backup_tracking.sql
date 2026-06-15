-- Create "audit_logs" table
CREATE TABLE "audit_logs" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "outlet_id" uuid NULL, "actor_user_id" uuid NOT NULL, "approver_user_id" uuid NULL, "action" character varying NOT NULL, "entity_type" character varying NULL, "entity_id" character varying NULL, "reason" text NULL, "before_json" jsonb NULL, "after_json" jsonb NULL, "amount" double precision NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "auditlog_tenant_id_actor_user_id" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id_actor_user_id" ON "audit_logs" ("tenant_id", "actor_user_id");
-- Create index "auditlog_tenant_id_created_at" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id_created_at" ON "audit_logs" ("tenant_id", "created_at");
-- Create index "auditlog_tenant_id_outlet_id_action" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id_outlet_id_action" ON "audit_logs" ("tenant_id", "outlet_id", "action");
-- Create "backups" table
CREATE TABLE "backups" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "path" character varying NOT NULL, "size_bytes" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'completed', "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "backup_created_at" to table: "backups"
CREATE INDEX "backup_created_at" ON "backups" ("created_at");
-- Create index "backup_tenant_id_created_at" to table: "backups"
CREATE INDEX "backup_tenant_id_created_at" ON "backups" ("tenant_id", "created_at");
-- Create "stock_counts" table
CREATE TABLE "stock_counts" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "reference" character varying NULL, "status" character varying NOT NULL DEFAULT 'draft', "counted_by" uuid NULL, "approved_by" uuid NULL, "notes" text NULL, "approved_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockcount_tenant_id_status" to table: "stock_counts"
CREATE INDEX "stockcount_tenant_id_status" ON "stock_counts" ("tenant_id", "status");
-- Create index "stockcount_tenant_id_warehouse_id" to table: "stock_counts"
CREATE INDEX "stockcount_tenant_id_warehouse_id" ON "stock_counts" ("tenant_id", "warehouse_id");
-- Create "stock_count_lines" table
CREATE TABLE "stock_count_lines" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "stock_count_id" uuid NOT NULL, "item_id" uuid NOT NULL, "sku" character varying NOT NULL, "system_qty" double precision NOT NULL DEFAULT 0, "counted_qty" double precision NULL, "variance" double precision NULL, "posted" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockcountline_stock_count_id_item_id" to table: "stock_count_lines"
CREATE UNIQUE INDEX "stockcountline_stock_count_id_item_id" ON "stock_count_lines" ("stock_count_id", "item_id");
-- Create index "stockcountline_tenant_id_stock_count_id" to table: "stock_count_lines"
CREATE INDEX "stockcountline_tenant_id_stock_count_id" ON "stock_count_lines" ("tenant_id", "stock_count_id");
-- Create "user_outlets" table
CREATE TABLE "user_outlets" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "user_id" uuid NOT NULL, "outlet_id" uuid NOT NULL, "is_home_outlet" boolean NOT NULL DEFAULT false, "assigned_by" uuid NULL, "assigned_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "useroutlet_tenant_id_outlet_id" to table: "user_outlets"
CREATE INDEX "useroutlet_tenant_id_outlet_id" ON "user_outlets" ("tenant_id", "outlet_id");
-- Create index "useroutlet_tenant_id_user_id" to table: "user_outlets"
CREATE INDEX "useroutlet_tenant_id_user_id" ON "user_outlets" ("tenant_id", "user_id");
-- Create index "useroutlet_tenant_id_user_id_outlet_id" to table: "user_outlets"
CREATE UNIQUE INDEX "useroutlet_tenant_id_user_id_outlet_id" ON "user_outlets" ("tenant_id", "user_id", "outlet_id");
