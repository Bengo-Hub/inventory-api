-- Carry the project onto awarded supplier contracts (from the source RFQ).
ALTER TABLE "contracts" ADD COLUMN "project_id" uuid NULL;
