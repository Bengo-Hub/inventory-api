-- Create index "modifiergroup_tenant_id_item_id_name" to table: "modifier_groups"
CREATE UNIQUE INDEX "modifiergroup_tenant_id_item_id_name" ON "modifier_groups" ("tenant_id", "item_id", "name");
-- Create index "modifieroption_group_id_name" to table: "modifier_options"
CREATE UNIQUE INDEX "modifieroption_group_id_name" ON "modifier_options" ("group_id", "name");
