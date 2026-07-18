-- Modify "item_categories" table
ALTER TABLE "item_categories" ADD COLUMN "use_cases" jsonb NULL;
-- Modify "units" table
ALTER TABLE "units" ADD COLUMN "use_cases" jsonb NULL;
