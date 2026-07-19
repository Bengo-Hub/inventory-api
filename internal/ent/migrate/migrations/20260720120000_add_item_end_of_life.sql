-- Modify "items" table: add End-of-Life (EOL) lifecycle timestamp (nullable; non-null = marked EOL)
ALTER TABLE "items" ADD COLUMN IF NOT EXISTS "end_of_life_at" timestamptz NULL;
-- Create index "item_tenant_id_end_of_life_at" to table: "items" (drives the EOL listing + purge scan)
CREATE INDEX IF NOT EXISTS "item_tenant_id_end_of_life_at" ON "items" ("tenant_id", "end_of_life_at");
