-- Create "bulk_jobs" table
-- Tracks a background bulk operation (item relocation, bulk stock adjustment, and any future
-- bulk action adopting the same pattern) so a large batch runs off the request/response cycle
-- with bounded concurrency, progress tracking, and a completion notification.
CREATE TABLE "bulk_jobs" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "job_type" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'queued', "total" bigint NOT NULL DEFAULT 0, "processed" bigint NOT NULL DEFAULT 0, "failed_count" bigint NOT NULL DEFAULT 0, "payload" jsonb NULL, "result" jsonb NULL, "created_by" uuid NULL, "error" text NULL, "created_at" timestamptz NOT NULL, "started_at" timestamptz NULL, "completed_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "bulkjob_tenant_id_created_at" to table: "bulk_jobs"
CREATE INDEX "bulkjob_tenant_id_created_at" ON "bulk_jobs" ("tenant_id", "created_at");
-- Create index "bulkjob_tenant_id_status" to table: "bulk_jobs"
CREATE INDEX "bulkjob_tenant_id_status" ON "bulk_jobs" ("tenant_id", "status");
