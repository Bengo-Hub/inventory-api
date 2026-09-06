-- Create "stock_clearances" table
CREATE TABLE "stock_clearances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "markdown_price" double precision NOT NULL, "reference_before" timestamptz NOT NULL, "starts_at" timestamptz NOT NULL, "ends_at" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'active', "ended_at" timestamptz NULL, "created_by" uuid NULL, "notes" character varying NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockclearance_tenant_item_lookup" to table: "stock_clearances"
CREATE INDEX "stockclearance_tenant_item_lookup" ON "stock_clearances" ("tenant_id", "item_id");
-- At most one ACTIVE clearance per item at a time (see itempricing_active_no_outlet for the
-- production race this style of partial-unique-index guard closes).
CREATE UNIQUE INDEX "stockclearance_active_unique" ON "stock_clearances" ("tenant_id", "item_id") WHERE "status" = 'active';
