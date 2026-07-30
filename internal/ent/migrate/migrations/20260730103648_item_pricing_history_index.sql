-- Drop the old 4-column unique index: it would reject the new supersede-insert history pattern
-- (a second row for the same tenant/item/tier/outlet at a different effective_from) with a
-- duplicate-key error. Superseded by the 5-column unique index below, which is a strict superset
-- and therefore weaker — every row that satisfied the old constraint still satisfies the new one.
DROP INDEX "itempricing_tenant_id_item_id_pricing_tier_id_outlet_id";
-- Create index "itempricing_tenant_id_item_id__05a7eb9c8fe5dc14922eea0b12d453de" to table: "item_pricings"
CREATE INDEX "itempricing_tenant_id_item_id__05a7eb9c8fe5dc14922eea0b12d453de" ON "item_pricings" ("tenant_id", "item_id", "pricing_tier_id", "outlet_id", "is_active");
-- Create index "itempricing_tenant_id_item_id__b39959a32066be1bff5d33d6c7ebf2ff" to table: "item_pricings"
CREATE UNIQUE INDEX "itempricing_tenant_id_item_id__b39959a32066be1bff5d33d6c7ebf2ff" ON "item_pricings" ("tenant_id", "item_id", "pricing_tier_id", "outlet_id", "effective_from");
