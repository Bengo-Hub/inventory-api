-- Add optional location_id FK to inventory_balances for sub-warehouse location tracking
ALTER TABLE "inventory_balances" ADD COLUMN "location_id" uuid NULL;
ALTER TABLE "inventory_balances" ADD CONSTRAINT "inventory_balances_warehouse_locations_location" FOREIGN KEY ("location_id") REFERENCES "warehouse_locations" ("id") ON DELETE SET NULL;
