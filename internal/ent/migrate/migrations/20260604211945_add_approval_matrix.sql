-- Create "approval_requests" table
CREATE TABLE "approval_requests" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "module" character varying NOT NULL, "object_id" uuid NOT NULL, "object_reference" character varying NULL, "amount" double precision NOT NULL DEFAULT 0, "rule_id" uuid NULL, "status" character varying NOT NULL DEFAULT 'pending', "current_sequence" bigint NOT NULL DEFAULT 1, "submitted_by" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "decided_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "approvalrequest_tenant_id_module_object_id" to table: "approval_requests"
CREATE INDEX "approvalrequest_tenant_id_module_object_id" ON "approval_requests" ("tenant_id", "module", "object_id");
-- Create index "approvalrequest_tenant_id_status" to table: "approval_requests"
CREATE INDEX "approvalrequest_tenant_id_status" ON "approval_requests" ("tenant_id", "status");
-- Create "approval_actions" table
CREATE TABLE "approval_actions" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "sequence" bigint NOT NULL, "name" character varying NULL, "approver_role" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'pending', "acted_by" uuid NULL, "acted_at" timestamptz NULL, "comment" text NULL, "created_at" timestamptz NOT NULL, "request_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "approval_actions_approval_requests_actions" FOREIGN KEY ("request_id") REFERENCES "approval_requests" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "approvalaction_request_id_sequence" to table: "approval_actions"
CREATE INDEX "approvalaction_request_id_sequence" ON "approval_actions" ("request_id", "sequence");
-- Create index "approvalaction_tenant_id" to table: "approval_actions"
CREATE INDEX "approvalaction_tenant_id" ON "approval_actions" ("tenant_id");
-- Create "approval_rules" table
CREATE TABLE "approval_rules" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "module" character varying NOT NULL, "name" character varying NOT NULL, "min_amount" double precision NOT NULL DEFAULT 0, "max_amount" double precision NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "approvalrule_tenant_id_module_is_active" to table: "approval_rules"
CREATE INDEX "approvalrule_tenant_id_module_is_active" ON "approval_rules" ("tenant_id", "module", "is_active");
-- Create "approval_steps" table
CREATE TABLE "approval_steps" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "sequence" bigint NOT NULL, "name" character varying NOT NULL, "approver_role" character varying NOT NULL, "created_at" timestamptz NOT NULL, "rule_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "approval_steps_approval_rules_steps" FOREIGN KEY ("rule_id") REFERENCES "approval_rules" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "approvalstep_rule_id_sequence" to table: "approval_steps"
CREATE UNIQUE INDEX "approvalstep_rule_id_sequence" ON "approval_steps" ("rule_id", "sequence");
-- Create index "approvalstep_tenant_id" to table: "approval_steps"
CREATE INDEX "approvalstep_tenant_id" ON "approval_steps" ("tenant_id");
