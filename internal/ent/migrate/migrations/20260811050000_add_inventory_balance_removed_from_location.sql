-- Modify "inventory_balances" table
-- True when this (item, warehouse) balance was explicitly moved/removed away (a transfer
-- shipped its last unit out) rather than merely sold to zero. Distinct from an organic
-- stock-out, which must keep the item visible for reordering.
ALTER TABLE "inventory_balances" ADD COLUMN IF NOT EXISTS "removed_from_location" boolean NOT NULL DEFAULT false;
