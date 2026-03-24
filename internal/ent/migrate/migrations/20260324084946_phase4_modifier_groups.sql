-- Create "modifier_groups" table
CREATE TABLE "modifier_groups" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "is_required" boolean NOT NULL DEFAULT false, "min_selections" bigint NOT NULL DEFAULT 0, "max_selections" bigint NOT NULL DEFAULT 1, "display_order" bigint NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "modifier_groups_items_modifier_groups" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "modifiergroup_item_id_display_order" to table: "modifier_groups"
CREATE INDEX "modifiergroup_item_id_display_order" ON "modifier_groups" ("item_id", "display_order");
-- Create index "modifiergroup_tenant_id_item_id" to table: "modifier_groups"
CREATE INDEX "modifiergroup_tenant_id_item_id" ON "modifier_groups" ("tenant_id", "item_id");
-- Create "modifier_options" table
CREATE TABLE "modifier_options" ("id" uuid NOT NULL, "name" character varying NOT NULL, "sku" character varying NULL, "price_adjustment" double precision NOT NULL DEFAULT 0, "is_default" boolean NOT NULL DEFAULT false, "is_active" boolean NOT NULL DEFAULT true, "display_order" bigint NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "group_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "modifier_options_modifier_groups_options" FOREIGN KEY ("group_id") REFERENCES "modifier_groups" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "modifieroption_group_id_display_order" to table: "modifier_options"
CREATE INDEX "modifieroption_group_id_display_order" ON "modifier_options" ("group_id", "display_order");
