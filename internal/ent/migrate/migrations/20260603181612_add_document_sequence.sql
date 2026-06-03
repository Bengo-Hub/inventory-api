-- Create "document_sequences" table
CREATE TABLE "document_sequences" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "doc_type" character varying NOT NULL, "prefix" character varying NULL, "separator" character varying NOT NULL DEFAULT '-', "date_format" character varying NULL, "padding" bigint NOT NULL DEFAULT 6, "reset_freq" character varying NOT NULL DEFAULT 'never', "current_val" bigint NOT NULL DEFAULT 0, "last_reset" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "documentsequence_tenant_id_doc_type" to table: "document_sequences"
CREATE UNIQUE INDEX "documentsequence_tenant_id_doc_type" ON "document_sequences" ("tenant_id", "doc_type");
-- Create "tickets" table
CREATE TABLE "tickets" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "event_item_id" uuid NOT NULL, "order_id" uuid NULL, "tier_id" character varying NULL, "tier_name" character varying NULL, "buyer_id" uuid NULL, "buyer_name" character varying NULL, "buyer_email" character varying NULL, "quantity" bigint NOT NULL DEFAULT 1, "unit_price" double precision NOT NULL DEFAULT 0, "total_price" double precision NOT NULL DEFAULT 0, "currency" character varying NOT NULL DEFAULT 'KES', "code" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'issued', "valid_from" timestamptz NULL, "valid_until" timestamptz NULL, "redeemed_at" timestamptz NULL, "redeemed_by" uuid NULL, "check_in_location" character varying NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "ticket_tenant_id_buyer_id_status" to table: "tickets"
CREATE INDEX "ticket_tenant_id_buyer_id_status" ON "tickets" ("tenant_id", "buyer_id", "status");
-- Create index "ticket_tenant_id_code" to table: "tickets"
CREATE UNIQUE INDEX "ticket_tenant_id_code" ON "tickets" ("tenant_id", "code");
-- Create index "ticket_tenant_id_event_item_id_status" to table: "tickets"
CREATE INDEX "ticket_tenant_id_event_item_id_status" ON "tickets" ("tenant_id", "event_item_id", "status");
-- Create index "ticket_tenant_id_order_id" to table: "tickets"
CREATE INDEX "ticket_tenant_id_order_id" ON "tickets" ("tenant_id", "order_id");
