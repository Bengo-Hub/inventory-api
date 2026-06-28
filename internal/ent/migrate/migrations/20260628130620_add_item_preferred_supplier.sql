-- Procure-to-order: add a preferred supplier to items so the consumer can split a draft PO
-- per vendor. Optional/nillable FK to suppliers; ON DELETE SET NULL so removing a supplier
-- leaves the item unassigned (collected into the supplier-less draft PO).
-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "preferred_supplier_id" uuid NULL, ADD CONSTRAINT "items_suppliers_preferred_items" FOREIGN KEY ("preferred_supplier_id") REFERENCES "suppliers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "item_tenant_id_preferred_supplier_id" to table: "items"
CREATE INDEX "item_tenant_id_preferred_supplier_id" ON "items" ("tenant_id", "preferred_supplier_id");
