-- Create "expiry_alert_logs" table
CREATE TABLE "expiry_alert_logs" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "lot_id" uuid NOT NULL, "tier" character varying NOT NULL, "alerted_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "expiryalertlog_tenant_id_lot_id_tier" to table: "expiry_alert_logs"
CREATE UNIQUE INDEX "expiryalertlog_tenant_id_lot_id_tier" ON "expiry_alert_logs" ("tenant_id", "lot_id", "tier");
