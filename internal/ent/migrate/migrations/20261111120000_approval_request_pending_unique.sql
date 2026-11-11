-- Close duplicate pending approval requests (keep the newest per tenant, module and object).
-- cmd/migrate runs this same statement through migrate.PreMigrate before the schema diff,
-- because Schema.Create does not execute these files.
UPDATE "approval_requests" ar SET "status" = 'cancelled', "decided_at" = now(), "updated_at" = now() WHERE ar."status" = 'pending' AND EXISTS (SELECT 1 FROM "approval_requests" o WHERE o."tenant_id" = ar."tenant_id" AND o."module" = ar."module" AND o."object_id" = ar."object_id" AND o."status" = 'pending' AND (o."created_at" > ar."created_at" OR (o."created_at" = ar."created_at" AND o."id" > ar."id")));
-- Create index "approvalrequest_pending_unique" to table: "approval_requests"
CREATE UNIQUE INDEX "approvalrequest_pending_unique" ON "approval_requests" ("tenant_id", "module", "object_id") WHERE (status = 'pending');
