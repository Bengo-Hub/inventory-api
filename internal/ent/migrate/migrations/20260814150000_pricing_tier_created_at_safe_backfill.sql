-- pricing_tiers.created_at was added to the ent schema without a migration ever being
-- generated for it; a bare NOT NULL ADD COLUMN fails on an already-populated table (no SQL
-- default is emitted for a Go-level time.Now default). Safe three-step pattern: add nullable,
-- backfill, then enforce NOT NULL — matches the documented recipe for this exact failure class.
ALTER TABLE "pricing_tiers" ADD COLUMN IF NOT EXISTS "created_at" timestamptz NULL;
UPDATE "pricing_tiers" SET "created_at" = now() WHERE "created_at" IS NULL;
ALTER TABLE "pricing_tiers" ALTER COLUMN "created_at" SET NOT NULL;
