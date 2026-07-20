-- Modify "approval_requests" table
ALTER TABLE "approval_requests" ADD COLUMN "payload" jsonb NULL, ADD COLUMN "executed_at" timestamptz NULL;
