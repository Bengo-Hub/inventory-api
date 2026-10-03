-- Modify "consumption_lines" table (columns already exist in production; IF NOT EXISTS keeps this idempotent)
ALTER TABLE "consumption_lines" ADD COLUMN IF NOT EXISTS "order_number" character varying NULL, ADD COLUMN IF NOT EXISTS "customer_name" character varying NULL, ADD COLUMN IF NOT EXISTS "customer_phone" character varying NULL, ADD COLUMN IF NOT EXISTS "served_by_user_id" uuid NULL, ADD COLUMN IF NOT EXISTS "served_by_name" character varying NULL;
-- Modify "tenant_inventory_configs" table
ALTER TABLE "tenant_inventory_configs" ADD COLUMN IF NOT EXISTS "auto_hide_on_stock_out" boolean NOT NULL DEFAULT false;
