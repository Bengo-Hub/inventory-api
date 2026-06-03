-- Modify "inventory_roles" table
ALTER TABLE "inventory_roles" ALTER COLUMN "tenant_id" DROP NOT NULL;
-- Create index "inventoryrole_role_code" to table: "inventory_roles"
CREATE INDEX "inventoryrole_role_code" ON "inventory_roles" ("role_code");
