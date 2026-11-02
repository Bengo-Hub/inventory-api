-- Create index "consumption_tenant_id_created_at" to table: "consumptions"
CREATE INDEX "consumption_tenant_id_created_at" ON "consumptions" ("tenant_id", "created_at");
