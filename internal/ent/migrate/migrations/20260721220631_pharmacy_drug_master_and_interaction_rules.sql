-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "generic_name" character varying NULL, ADD COLUMN "active_ingredient" character varying NULL, ADD COLUMN "dosage_form" character varying NULL, ADD COLUMN "strength" character varying NULL, ADD COLUMN "drug_class" character varying NULL, ADD COLUMN "controlled_substance_schedule" character varying NOT NULL DEFAULT 'NONE';
-- Create "drug_interaction_rules" table
CREATE TABLE "drug_interaction_rules" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "is_global" boolean NOT NULL DEFAULT false, "class_a" character varying NOT NULL, "class_b" character varying NOT NULL, "severity" character varying NOT NULL DEFAULT 'moderate', "description" text NULL, "clinical_recommendation" text NULL, "source" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "druginteractionrule_is_global" to table: "drug_interaction_rules"
CREATE INDEX "druginteractionrule_is_global" ON "drug_interaction_rules" ("is_global");
-- Create index "druginteractionrule_tenant_id_class_a_class_b" to table: "drug_interaction_rules"
CREATE UNIQUE INDEX "druginteractionrule_tenant_id_class_a_class_b" ON "drug_interaction_rules" ("tenant_id", "class_a", "class_b");
-- Create index "druginteractionrule_tenant_id_is_active" to table: "drug_interaction_rules"
CREATE INDEX "druginteractionrule_tenant_id_is_active" ON "drug_interaction_rules" ("tenant_id", "is_active");
