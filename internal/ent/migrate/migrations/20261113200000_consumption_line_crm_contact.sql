-- Modify "consumption_lines" table
ALTER TABLE "consumption_lines" ADD COLUMN "crm_contact_id" uuid NULL;
-- Create index "consumptionline_tenant_id_crm_contact_id" to table: "consumption_lines"
CREATE INDEX "consumptionline_tenant_id_crm_contact_id" ON "consumption_lines" ("tenant_id", "crm_contact_id");
