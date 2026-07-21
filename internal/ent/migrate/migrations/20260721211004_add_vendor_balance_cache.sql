-- Create "vendor_balance_caches" table
CREATE TABLE "vendor_balance_caches" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "vendor_id" uuid NULL, "vendor_identifier" character varying NULL, "vendor_name" character varying NULL, "balance_owed" character varying NOT NULL DEFAULT '0', "outstanding_payable" character varying NOT NULL DEFAULT '0', "currency" character varying NOT NULL DEFAULT 'KES', "synced_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "vendorbalancecache_tenant_id_vendor_id" to table: "vendor_balance_caches"
CREATE UNIQUE INDEX "vendorbalancecache_tenant_id_vendor_id" ON "vendor_balance_caches" ("tenant_id", "vendor_id");
-- Create index "vendorbalancecache_tenant_id_vendor_identifier" to table: "vendor_balance_caches"
CREATE INDEX "vendorbalancecache_tenant_id_vendor_identifier" ON "vendor_balance_caches" ("tenant_id", "vendor_identifier");
