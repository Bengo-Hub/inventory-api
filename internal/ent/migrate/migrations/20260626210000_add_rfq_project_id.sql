-- Carry the project through the Requisition -> RFQ -> award -> PO chain.
ALTER TABLE "rf_qs" ADD COLUMN "project_id" uuid NULL;
CREATE INDEX "rfq_tenant_id_project_id" ON "rf_qs" ("tenant_id", "project_id");
