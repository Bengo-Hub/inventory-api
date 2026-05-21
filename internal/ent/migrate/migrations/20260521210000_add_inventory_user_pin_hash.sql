-- Add pin_hash column to inventory_users for cross-service PIN sync
ALTER TABLE "inventory_users"
    ADD COLUMN IF NOT EXISTS "pin_hash" character varying NULL;
