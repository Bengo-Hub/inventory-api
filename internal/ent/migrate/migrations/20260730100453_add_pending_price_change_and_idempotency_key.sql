-- Modify "goods_receipt_lines" table
ALTER TABLE "goods_receipt_lines" ADD COLUMN "new_selling_price" double precision NULL, ADD COLUMN "price_scope" character varying NULL DEFAULT 'all_stock';
-- Create "idempotency_keys" table
CREATE TABLE "idempotency_keys" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "key" character varying NOT NULL, "endpoint" character varying NOT NULL DEFAULT '', "status" character varying NOT NULL DEFAULT 'in_flight', "response_code" bigint NOT NULL DEFAULT 0, "response_body" bytea NULL, "created_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "idempotencykey_expires_at" to table: "idempotency_keys"
CREATE INDEX "idempotencykey_expires_at" ON "idempotency_keys" ("expires_at");
-- Create index "idempotencykey_tenant_id_key" to table: "idempotency_keys"
CREATE UNIQUE INDEX "idempotencykey_tenant_id_key" ON "idempotency_keys" ("tenant_id", "key");
-- Create "pending_price_changes" table
CREATE TABLE "pending_price_changes" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "new_price" double precision NOT NULL, "currency" character varying NOT NULL DEFAULT 'KES', "trigger_before" timestamptz NOT NULL, "reason" text NULL, "created_by" uuid NULL, "goods_receipt_line_id" uuid NULL, "status" character varying NOT NULL DEFAULT 'pending', "created_at" timestamptz NOT NULL, "applied_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "pendingpricechange_goods_receipt_line_id" to table: "pending_price_changes"
CREATE INDEX "pendingpricechange_goods_receipt_line_id" ON "pending_price_changes" ("goods_receipt_line_id");
-- Create index "pendingpricechange_tenant_id_item_id_status" to table: "pending_price_changes"
CREATE INDEX "pendingpricechange_tenant_id_item_id_status" ON "pending_price_changes" ("tenant_id", "item_id", "status");
