-- Modify "inventory_balances" table
ALTER TABLE "inventory_balances" ADD COLUMN "reorder_level" bigint NOT NULL DEFAULT 1;
-- Modify "item_categories" table
ALTER TABLE "item_categories" ADD COLUMN "code" character varying NULL;
-- Create "outbox_events" table
CREATE TABLE "outbox_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "aggregate_type" character varying NOT NULL, "aggregate_id" character varying NOT NULL, "event_type" character varying NOT NULL, "payload" jsonb NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "attempts" bigint NOT NULL DEFAULT 0, "last_attempt_at" timestamptz NULL, "published_at" timestamptz NULL, "error_message" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "outboxevent_created_at" to table: "outbox_events"
CREATE INDEX "outboxevent_created_at" ON "outbox_events" ("created_at");
-- Create index "outboxevent_status" to table: "outbox_events"
CREATE INDEX "outboxevent_status" ON "outbox_events" ("status");
-- Create index "outboxevent_tenant_id_status" to table: "outbox_events"
CREATE INDEX "outboxevent_tenant_id_status" ON "outbox_events" ("tenant_id", "status");
-- Create "item_assets" table
CREATE TABLE "item_assets" ("id" uuid NOT NULL, "asset_type" character varying NOT NULL, "url" character varying NOT NULL, "file_name" character varying NULL, "file_size" character varying NULL, "mime_type" character varying NULL, "metadata" jsonb NULL, "display_order" bigint NOT NULL DEFAULT 0, "is_primary" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_assets_items_assets" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemasset_asset_type" to table: "item_assets"
CREATE INDEX "itemasset_asset_type" ON "item_assets" ("asset_type");
-- Create index "itemasset_is_primary" to table: "item_assets"
CREATE INDEX "itemasset_is_primary" ON "item_assets" ("is_primary");
-- Create index "itemasset_item_id" to table: "item_assets"
CREATE INDEX "itemasset_item_id" ON "item_assets" ("item_id");
