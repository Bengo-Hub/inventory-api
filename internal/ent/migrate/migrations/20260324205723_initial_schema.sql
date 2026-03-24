-- Create "tenants" table
CREATE TABLE "tenants" ("id" uuid NOT NULL, "name" character varying NOT NULL, "slug" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "use_case" character varying NULL, "sync_status" character varying NOT NULL DEFAULT 'synced', "last_sync_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "tenant_slug" to table: "tenants"
CREATE UNIQUE INDEX "tenant_slug" ON "tenants" ("slug");
-- Create index "tenant_status" to table: "tenants"
CREATE INDEX "tenant_status" ON "tenants" ("status");
-- Create index "tenants_slug_key" to table: "tenants"
CREATE UNIQUE INDEX "tenants_slug_key" ON "tenants" ("slug");
-- Create "rate_limit_configs" table
CREATE TABLE "rate_limit_configs" ("id" uuid NOT NULL, "service_name" character varying NOT NULL, "key_type" character varying NOT NULL, "endpoint_pattern" character varying NOT NULL DEFAULT '*', "requests_per_window" bigint NOT NULL DEFAULT 60, "window_seconds" bigint NOT NULL DEFAULT 60, "burst_multiplier" double precision NOT NULL DEFAULT 1.5, "is_active" boolean NOT NULL DEFAULT true, "description" character varying NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "ratelimitconfig_is_active" to table: "rate_limit_configs"
CREATE INDEX "ratelimitconfig_is_active" ON "rate_limit_configs" ("is_active");
-- Create index "ratelimitconfig_service_name" to table: "rate_limit_configs"
CREATE INDEX "ratelimitconfig_service_name" ON "rate_limit_configs" ("service_name");
-- Create index "ratelimitconfig_service_name_key_type_endpoint_pattern" to table: "rate_limit_configs"
CREATE UNIQUE INDEX "ratelimitconfig_service_name_key_type_endpoint_pattern" ON "rate_limit_configs" ("service_name", "key_type", "endpoint_pattern");
-- Create "consumptions" table
CREATE TABLE "consumptions" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "order_id" uuid NOT NULL, "warehouse_id" uuid NULL, "items" jsonb NOT NULL, "reason" character varying NOT NULL DEFAULT 'sale', "status" character varying NOT NULL DEFAULT 'processed', "idempotency_key" character varying NULL, "processed_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "consumption_idempotency_key" to table: "consumptions"
CREATE UNIQUE INDEX "consumption_idempotency_key" ON "consumptions" ("idempotency_key");
-- Create index "consumption_tenant_id_order_id" to table: "consumptions"
CREATE INDEX "consumption_tenant_id_order_id" ON "consumptions" ("tenant_id", "order_id");
-- Create "variant_attributes" table
CREATE TABLE "variant_attributes" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "values" jsonb NOT NULL, "sort_order" bigint NOT NULL DEFAULT 0, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "variantattribute_tenant_id_name" to table: "variant_attributes"
CREATE UNIQUE INDEX "variantattribute_tenant_id_name" ON "variant_attributes" ("tenant_id", "name");
-- Create "outbox_events" table
CREATE TABLE "outbox_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "aggregate_type" character varying NOT NULL, "aggregate_id" character varying NOT NULL, "event_type" character varying NOT NULL, "payload" jsonb NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "attempts" bigint NOT NULL DEFAULT 0, "last_attempt_at" timestamptz NULL, "published_at" timestamptz NULL, "error_message" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "outboxevent_created_at" to table: "outbox_events"
CREATE INDEX "outboxevent_created_at" ON "outbox_events" ("created_at");
-- Create index "outboxevent_status" to table: "outbox_events"
CREATE INDEX "outboxevent_status" ON "outbox_events" ("status");
-- Create index "outboxevent_tenant_id_status" to table: "outbox_events"
CREATE INDEX "outboxevent_tenant_id_status" ON "outbox_events" ("tenant_id", "status");
-- Create "stock_adjustments" table
CREATE TABLE "stock_adjustments" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, "quantity_before" double precision NOT NULL, "quantity_change" double precision NOT NULL, "quantity_after" double precision NOT NULL, "reason" character varying NOT NULL, "reference" character varying NULL, "notes" text NULL, "adjusted_by" uuid NOT NULL, "adjusted_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stockadjustment_adjusted_at" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_adjusted_at" ON "stock_adjustments" ("adjusted_at");
-- Create index "stockadjustment_tenant_id_item_id" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_item_id" ON "stock_adjustments" ("tenant_id", "item_id");
-- Create index "stockadjustment_tenant_id_reason" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_reason" ON "stock_adjustments" ("tenant_id", "reason");
-- Create index "stockadjustment_tenant_id_warehouse_id" to table: "stock_adjustments"
CREATE INDEX "stockadjustment_tenant_id_warehouse_id" ON "stock_adjustments" ("tenant_id", "warehouse_id");
-- Create "service_configs" table
CREATE TABLE "service_configs" ("id" uuid NOT NULL, "tenant_id" uuid NULL, "config_key" character varying NOT NULL, "config_value" text NOT NULL, "config_type" character varying NOT NULL DEFAULT 'string', "description" character varying NULL, "is_secret" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "serviceconfig_config_key" to table: "service_configs"
CREATE INDEX "serviceconfig_config_key" ON "service_configs" ("config_key");
-- Create index "serviceconfig_tenant_id_config_key" to table: "service_configs"
CREATE UNIQUE INDEX "serviceconfig_tenant_id_config_key" ON "service_configs" ("tenant_id", "config_key");
-- Create "item_categories" table
CREATE TABLE "item_categories" ("id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NULL, "slug" character varying NULL, "icon" character varying NULL, "description" text NULL, "depth" bigint NOT NULL DEFAULT 0, "path" character varying NULL, "sort_order" bigint NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "parent_id" uuid NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_categories_item_categories_children" FOREIGN KEY ("parent_id") REFERENCES "item_categories" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "item_categories_tenants_item_categories" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemcategory_path" to table: "item_categories"
CREATE INDEX "itemcategory_path" ON "item_categories" ("path");
-- Create index "itemcategory_tenant_id_name" to table: "item_categories"
CREATE INDEX "itemcategory_tenant_id_name" ON "item_categories" ("tenant_id", "name");
-- Create index "itemcategory_tenant_id_parent_id" to table: "item_categories"
CREATE INDEX "itemcategory_tenant_id_parent_id" ON "item_categories" ("tenant_id", "parent_id");
-- Create index "itemcategory_tenant_id_sort_order" to table: "item_categories"
CREATE INDEX "itemcategory_tenant_id_sort_order" ON "item_categories" ("tenant_id", "sort_order");
-- Create "units" table
CREATE TABLE "units" ("id" uuid NOT NULL, "name" character varying NOT NULL, "abbreviation" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "unit_name" to table: "units"
CREATE UNIQUE INDEX "unit_name" ON "units" ("name");
-- Create index "units_name_key" to table: "units"
CREATE UNIQUE INDEX "units_name_key" ON "units" ("name");
-- Create "items" table
CREATE TABLE "items" ("id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "type" character varying NOT NULL DEFAULT 'GOODS', "is_active" boolean NOT NULL DEFAULT true, "image_url" character varying NULL, "barcode" character varying NULL, "barcode_type" character varying NULL, "requires_age_verification" boolean NOT NULL DEFAULT false, "is_controlled_substance" boolean NOT NULL DEFAULT false, "is_perishable" boolean NOT NULL DEFAULT false, "track_serial_numbers" boolean NOT NULL DEFAULT false, "track_lots" boolean NOT NULL DEFAULT false, "weight_kg" double precision NULL, "dimensions_cm" jsonb NULL, "duration_minutes" bigint NULL, "tags" jsonb NOT NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "unit_id" uuid NULL, "category_id" uuid NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "items_item_categories_items" FOREIGN KEY ("category_id") REFERENCES "item_categories" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "items_tenants_items" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "items_units_units" FOREIGN KEY ("unit_id") REFERENCES "units" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "item_tenant_id_barcode" to table: "items"
CREATE INDEX "item_tenant_id_barcode" ON "items" ("tenant_id", "barcode");
-- Create index "item_tenant_id_category_id" to table: "items"
CREATE INDEX "item_tenant_id_category_id" ON "items" ("tenant_id", "category_id");
-- Create index "item_tenant_id_is_active" to table: "items"
CREATE INDEX "item_tenant_id_is_active" ON "items" ("tenant_id", "is_active");
-- Create index "item_tenant_id_sku" to table: "items"
CREATE UNIQUE INDEX "item_tenant_id_sku" ON "items" ("tenant_id", "sku");
-- Create "bundles" table
CREATE TABLE "bundles" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "bundles_items_bundle" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "bundle_tenant_id_is_active" to table: "bundles"
CREATE INDEX "bundle_tenant_id_is_active" ON "bundles" ("tenant_id", "is_active");
-- Create index "bundle_tenant_id_item_id" to table: "bundles"
CREATE UNIQUE INDEX "bundle_tenant_id_item_id" ON "bundles" ("tenant_id", "item_id");
-- Create index "bundles_item_id_key" to table: "bundles"
CREATE UNIQUE INDEX "bundles_item_id_key" ON "bundles" ("item_id");
-- Create "bundle_components" table
CREATE TABLE "bundle_components" ("id" uuid NOT NULL, "quantity" bigint NOT NULL DEFAULT 1, "sort_order" bigint NOT NULL DEFAULT 0, "bundle_id" uuid NOT NULL, "component_item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "bundle_components_bundles_components" FOREIGN KEY ("bundle_id") REFERENCES "bundles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "bundle_components_items_bundle_components" FOREIGN KEY ("component_item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "bundlecomponent_bundle_id_component_item_id" to table: "bundle_components"
CREATE UNIQUE INDEX "bundlecomponent_bundle_id_component_item_id" ON "bundle_components" ("bundle_id", "component_item_id");
-- Create "custom_field_definitions" table
CREATE TABLE "custom_field_definitions" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "field_key" character varying NOT NULL, "label" character varying NOT NULL, "field_type" character varying NOT NULL DEFAULT 'text', "enum_values" jsonb NULL, "is_required" boolean NOT NULL DEFAULT false, "sort_order" bigint NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "category_id" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "custom_field_definitions_item__8a64c8951c7b298cf00d71a062a24180" FOREIGN KEY ("category_id") REFERENCES "item_categories" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "customfielddefinition_tenant_id_category_id" to table: "custom_field_definitions"
CREATE INDEX "customfielddefinition_tenant_id_category_id" ON "custom_field_definitions" ("tenant_id", "category_id");
-- Create index "customfielddefinition_tenant_id_field_key" to table: "custom_field_definitions"
CREATE UNIQUE INDEX "customfielddefinition_tenant_id_field_key" ON "custom_field_definitions" ("tenant_id", "field_key");
-- Create index "customfielddefinition_tenant_id_is_active" to table: "custom_field_definitions"
CREATE INDEX "customfielddefinition_tenant_id_is_active" ON "custom_field_definitions" ("tenant_id", "is_active");
-- Create "custom_field_values" table
CREATE TABLE "custom_field_values" ("id" uuid NOT NULL, "value" character varying NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "field_definition_id" uuid NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "custom_field_values_custom_field_definitions_values" FOREIGN KEY ("field_definition_id") REFERENCES "custom_field_definitions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "custom_field_values_items_custom_field_values" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "customfieldvalue_item_id" to table: "custom_field_values"
CREATE INDEX "customfieldvalue_item_id" ON "custom_field_values" ("item_id");
-- Create index "customfieldvalue_item_id_field_definition_id" to table: "custom_field_values"
CREATE UNIQUE INDEX "customfieldvalue_item_id_field_definition_id" ON "custom_field_values" ("item_id", "field_definition_id");
-- Create "warehouses" table
CREATE TABLE "warehouses" ("id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "address" text NULL, "is_default" boolean NOT NULL DEFAULT false, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "warehouses_tenants_warehouses" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "warehouse_tenant_id_code" to table: "warehouses"
CREATE UNIQUE INDEX "warehouse_tenant_id_code" ON "warehouses" ("tenant_id", "code");
-- Create index "warehouse_tenant_id_is_active" to table: "warehouses"
CREATE INDEX "warehouse_tenant_id_is_active" ON "warehouses" ("tenant_id", "is_active");
-- Create index "warehouse_tenant_id_is_default" to table: "warehouses"
CREATE INDEX "warehouse_tenant_id_is_default" ON "warehouses" ("tenant_id", "is_default");
-- Create "inventory_balances" table
CREATE TABLE "inventory_balances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "on_hand" bigint NOT NULL DEFAULT 0, "available" bigint NOT NULL DEFAULT 0, "reserved" bigint NOT NULL DEFAULT 0, "unit_of_measure" character varying NOT NULL DEFAULT 'PIECE', "reorder_level" bigint NOT NULL DEFAULT 1, "reorder_quantity" bigint NOT NULL DEFAULT 0, "preferred_supplier_id" uuid NULL, "auto_reorder_enabled" boolean NOT NULL DEFAULT false, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "inventory_balances_items_balances" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "inventory_balances_warehouses_balances" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "inventorybalance_tenant_id_item_id" to table: "inventory_balances"
CREATE INDEX "inventorybalance_tenant_id_item_id" ON "inventory_balances" ("tenant_id", "item_id");
-- Create index "inventorybalance_tenant_id_item_id_warehouse_id" to table: "inventory_balances"
CREATE UNIQUE INDEX "inventorybalance_tenant_id_item_id_warehouse_id" ON "inventory_balances" ("tenant_id", "item_id", "warehouse_id");
-- Create "inventory_lots" table
CREATE TABLE "inventory_lots" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "lot_number" character varying NOT NULL, "expiry_date" timestamptz NULL, "manufactured_date" timestamptz NULL, "quantity" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'active', "cost_price" double precision NULL, "supplier_reference" character varying NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "inventory_lots_items_lots" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "inventory_lots_warehouses_lots" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "inventorylot_status" to table: "inventory_lots"
CREATE INDEX "inventorylot_status" ON "inventory_lots" ("status");
-- Create index "inventorylot_tenant_id_expiry_date" to table: "inventory_lots"
CREATE INDEX "inventorylot_tenant_id_expiry_date" ON "inventory_lots" ("tenant_id", "expiry_date");
-- Create index "inventorylot_tenant_id_item_id" to table: "inventory_lots"
CREATE INDEX "inventorylot_tenant_id_item_id" ON "inventory_lots" ("tenant_id", "item_id");
-- Create index "inventorylot_tenant_id_item_id_lot_number" to table: "inventory_lots"
CREATE UNIQUE INDEX "inventorylot_tenant_id_item_id_lot_number" ON "inventory_lots" ("tenant_id", "item_id", "lot_number");
-- Create "item_assets" table
CREATE TABLE "item_assets" ("id" uuid NOT NULL, "asset_type" character varying NOT NULL, "url" character varying NOT NULL, "file_name" character varying NULL, "file_size" character varying NULL, "mime_type" character varying NULL, "metadata" jsonb NULL, "display_order" bigint NOT NULL DEFAULT 0, "is_primary" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_assets_items_assets" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemasset_asset_type" to table: "item_assets"
CREATE INDEX "itemasset_asset_type" ON "item_assets" ("asset_type");
-- Create index "itemasset_is_primary" to table: "item_assets"
CREATE INDEX "itemasset_is_primary" ON "item_assets" ("is_primary");
-- Create index "itemasset_item_id" to table: "item_assets"
CREATE INDEX "itemasset_item_id" ON "item_assets" ("item_id");
-- Create "item_translations" table
CREATE TABLE "item_translations" ("id" uuid NOT NULL, "locale" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_translations_items_translations" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemtranslation_item_id_locale" to table: "item_translations"
CREATE UNIQUE INDEX "itemtranslation_item_id_locale" ON "item_translations" ("item_id", "locale");
-- Create "item_variants" table
CREATE TABLE "item_variants" ("id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "price" double precision NOT NULL DEFAULT 0, "attributes" jsonb NULL, "barcode" character varying NULL, "image_url" character varying NULL, "cost_price" double precision NULL, "weight_kg" double precision NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "item_variants_items_variants" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "itemvariant_barcode" to table: "item_variants"
CREATE INDEX "itemvariant_barcode" ON "item_variants" ("barcode");
-- Create index "itemvariant_item_id_sku" to table: "item_variants"
CREATE UNIQUE INDEX "itemvariant_item_id_sku" ON "item_variants" ("item_id", "sku");
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
-- Create "suppliers" table
CREATE TABLE "suppliers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "contact_name" character varying NULL, "contact_email" character varying NULL, "contact_phone" character varying NULL, "address" text NULL, "payment_terms" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "supplier_tenant_id_code" to table: "suppliers"
CREATE UNIQUE INDEX "supplier_tenant_id_code" ON "suppliers" ("tenant_id", "code");
-- Create index "supplier_tenant_id_is_active" to table: "suppliers"
CREATE INDEX "supplier_tenant_id_is_active" ON "suppliers" ("tenant_id", "is_active");
-- Create "purchase_orders" table
CREATE TABLE "purchase_orders" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "po_number" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'draft', "expected_date" timestamptz NULL, "total_amount" double precision NOT NULL DEFAULT 0, "currency" character varying NOT NULL DEFAULT 'KES', "notes" text NULL, "created_by" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "supplier_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "purchase_orders_suppliers_purchase_orders" FOREIGN KEY ("supplier_id") REFERENCES "suppliers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "purchase_orders_warehouses_purchase_orders" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "purchaseorder_tenant_id_po_number" to table: "purchase_orders"
CREATE UNIQUE INDEX "purchaseorder_tenant_id_po_number" ON "purchase_orders" ("tenant_id", "po_number");
-- Create index "purchaseorder_tenant_id_status" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_status" ON "purchase_orders" ("tenant_id", "status");
-- Create index "purchaseorder_tenant_id_supplier_id" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_supplier_id" ON "purchase_orders" ("tenant_id", "supplier_id");
-- Create "purchase_order_lines" table
CREATE TABLE "purchase_order_lines" ("id" uuid NOT NULL, "item_id" uuid NOT NULL, "variant_id" uuid NULL, "quantity_ordered" bigint NOT NULL DEFAULT 0, "quantity_received" bigint NOT NULL DEFAULT 0, "unit_price" double precision NOT NULL DEFAULT 0, "total_price" double precision NOT NULL DEFAULT 0, "po_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "purchase_order_lines_purchase_orders_lines" FOREIGN KEY ("po_id") REFERENCES "purchase_orders" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "purchaseorderline_po_id_item_id" to table: "purchase_order_lines"
CREATE UNIQUE INDEX "purchaseorderline_po_id_item_id" ON "purchase_order_lines" ("po_id", "item_id");
-- Create "recipes" table
CREATE TABLE "recipes" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "output_qty" double precision NOT NULL DEFAULT 1, "unit_of_measure" character varying NOT NULL DEFAULT 'PORTION', "is_active" boolean NOT NULL DEFAULT true, "total_cost" double precision NULL, "cost_per_portion" double precision NULL, "target_margin_percent" double precision NULL, "suggested_price" double precision NULL, "prep_time_minutes" bigint NULL, "metadata" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "recipe_tenant_id_is_active" to table: "recipes"
CREATE INDEX "recipe_tenant_id_is_active" ON "recipes" ("tenant_id", "is_active");
-- Create index "recipe_tenant_id_sku" to table: "recipes"
CREATE UNIQUE INDEX "recipe_tenant_id_sku" ON "recipes" ("tenant_id", "sku");
-- Create "recipe_ingredients" table
CREATE TABLE "recipe_ingredients" ("id" uuid NOT NULL, "item_sku" character varying NOT NULL, "quantity" double precision NOT NULL, "unit_of_measure" character varying NOT NULL DEFAULT 'PIECE', "notes" character varying NULL, "display_order" bigint NOT NULL DEFAULT 0, "item_id" uuid NOT NULL, "recipe_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "recipe_ingredients_items_recipe_ingredients" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "recipe_ingredients_recipes_ingredients" FOREIGN KEY ("recipe_id") REFERENCES "recipes" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "recipeingredient_item_sku" to table: "recipe_ingredients"
CREATE INDEX "recipeingredient_item_sku" ON "recipe_ingredients" ("item_sku");
-- Create index "recipeingredient_recipe_id" to table: "recipe_ingredients"
CREATE INDEX "recipeingredient_recipe_id" ON "recipe_ingredients" ("recipe_id");
-- Create index "recipeingredient_recipe_id_item_id" to table: "recipe_ingredients"
CREATE UNIQUE INDEX "recipeingredient_recipe_id_item_id" ON "recipe_ingredients" ("recipe_id", "item_id");
-- Create "reservations" table
CREATE TABLE "reservations" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "order_id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'pending', "items" jsonb NOT NULL, "expires_at" timestamptz NULL, "confirmed_at" timestamptz NULL, "idempotency_key" character varying NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "warehouse_id" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "reservations_warehouses_reservations" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "reservation_idempotency_key" to table: "reservations"
CREATE UNIQUE INDEX "reservation_idempotency_key" ON "reservations" ("idempotency_key");
-- Create index "reservation_tenant_id_order_id" to table: "reservations"
CREATE INDEX "reservation_tenant_id_order_id" ON "reservations" ("tenant_id", "order_id");
-- Create index "reservation_tenant_id_status" to table: "reservations"
CREATE INDEX "reservation_tenant_id_status" ON "reservations" ("tenant_id", "status");
-- Create "inventory_permissions" table
CREATE TABLE "inventory_permissions" ("id" uuid NOT NULL, "permission_code" character varying NOT NULL, "name" character varying NOT NULL, "module" character varying NOT NULL, "action" character varying NOT NULL, "resource" character varying NULL, "description" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "inventory_permissions_permission_code_key" to table: "inventory_permissions"
CREATE UNIQUE INDEX "inventory_permissions_permission_code_key" ON "inventory_permissions" ("permission_code");
-- Create index "inventorypermission_action" to table: "inventory_permissions"
CREATE INDEX "inventorypermission_action" ON "inventory_permissions" ("action");
-- Create index "inventorypermission_module" to table: "inventory_permissions"
CREATE INDEX "inventorypermission_module" ON "inventory_permissions" ("module");
-- Create index "inventorypermission_module_action" to table: "inventory_permissions"
CREATE INDEX "inventorypermission_module_action" ON "inventory_permissions" ("module", "action");
-- Create index "inventorypermission_permission_code" to table: "inventory_permissions"
CREATE UNIQUE INDEX "inventorypermission_permission_code" ON "inventory_permissions" ("permission_code");
-- Create "inventory_roles" table
CREATE TABLE "inventory_roles" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "role_code" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "is_system_role" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "inventoryrole_is_system_role" to table: "inventory_roles"
CREATE INDEX "inventoryrole_is_system_role" ON "inventory_roles" ("is_system_role");
-- Create index "inventoryrole_tenant_id" to table: "inventory_roles"
CREATE INDEX "inventoryrole_tenant_id" ON "inventory_roles" ("tenant_id");
-- Create index "inventoryrole_tenant_id_role_code" to table: "inventory_roles"
CREATE UNIQUE INDEX "inventoryrole_tenant_id_role_code" ON "inventory_roles" ("tenant_id", "role_code");
-- Create "role_permissions" table
CREATE TABLE "role_permissions" ("id" bigint NOT NULL GENERATED BY DEFAULT AS IDENTITY, "role_id" uuid NOT NULL, "permission_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "role_permissions_inventory_permissions_permission" FOREIGN KEY ("permission_id") REFERENCES "inventory_permissions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "role_permissions_inventory_roles_role" FOREIGN KEY ("role_id") REFERENCES "inventory_roles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "rolepermission_permission_id" to table: "role_permissions"
CREATE INDEX "rolepermission_permission_id" ON "role_permissions" ("permission_id");
-- Create index "rolepermission_role_id" to table: "role_permissions"
CREATE INDEX "rolepermission_role_id" ON "role_permissions" ("role_id");
-- Create index "rolepermission_role_id_permission_id" to table: "role_permissions"
CREATE UNIQUE INDEX "rolepermission_role_id_permission_id" ON "role_permissions" ("role_id", "permission_id");
-- Create "stock_transfers" table
CREATE TABLE "stock_transfers" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "source_warehouse_id" uuid NOT NULL, "destination_warehouse_id" uuid NOT NULL, "transfer_number" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'draft', "initiated_by" uuid NULL, "notes" text NULL, "shipped_at" timestamptz NULL, "received_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "stocktransfer_tenant_id_status" to table: "stock_transfers"
CREATE INDEX "stocktransfer_tenant_id_status" ON "stock_transfers" ("tenant_id", "status");
-- Create index "stocktransfer_tenant_id_transfer_number" to table: "stock_transfers"
CREATE UNIQUE INDEX "stocktransfer_tenant_id_transfer_number" ON "stock_transfers" ("tenant_id", "transfer_number");
-- Create "stock_transfer_lines" table
CREATE TABLE "stock_transfer_lines" ("id" uuid NOT NULL, "item_id" uuid NOT NULL, "variant_id" uuid NULL, "lot_id" uuid NULL, "quantity" bigint NOT NULL DEFAULT 0, "transfer_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "stock_transfer_lines_stock_transfers_lines" FOREIGN KEY ("transfer_id") REFERENCES "stock_transfers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "stocktransferline_transfer_id_item_id" to table: "stock_transfer_lines"
CREATE INDEX "stocktransferline_transfer_id_item_id" ON "stock_transfer_lines" ("transfer_id", "item_id");
-- Create "inventory_users" table
CREATE TABLE "inventory_users" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "auth_service_user_id" uuid NOT NULL, "email" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "sync_status" character varying NOT NULL DEFAULT 'synced', "last_sync_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "inventory_users_auth_service_user_id_key" to table: "inventory_users"
CREATE UNIQUE INDEX "inventory_users_auth_service_user_id_key" ON "inventory_users" ("auth_service_user_id");
-- Create index "inventoryuser_auth_service_user_id" to table: "inventory_users"
CREATE UNIQUE INDEX "inventoryuser_auth_service_user_id" ON "inventory_users" ("auth_service_user_id");
-- Create index "inventoryuser_status" to table: "inventory_users"
CREATE INDEX "inventoryuser_status" ON "inventory_users" ("status");
-- Create index "inventoryuser_sync_status" to table: "inventory_users"
CREATE INDEX "inventoryuser_sync_status" ON "inventory_users" ("sync_status");
-- Create index "inventoryuser_tenant_id" to table: "inventory_users"
CREATE INDEX "inventoryuser_tenant_id" ON "inventory_users" ("tenant_id");
-- Create index "inventoryuser_tenant_id_auth_service_user_id" to table: "inventory_users"
CREATE UNIQUE INDEX "inventoryuser_tenant_id_auth_service_user_id" ON "inventory_users" ("tenant_id", "auth_service_user_id");
-- Create "user_role_assignments" table
CREATE TABLE "user_role_assignments" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "assigned_by" uuid NOT NULL, "assigned_at" timestamptz NOT NULL, "expires_at" timestamptz NULL, "inventory_role_user_assignments" uuid NULL, "user_id" uuid NOT NULL, "role_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "user_role_assignments_inventory_roles_role" FOREIGN KEY ("role_id") REFERENCES "inventory_roles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "user_role_assignments_inventory_roles_user_assignments" FOREIGN KEY ("inventory_role_user_assignments") REFERENCES "inventory_roles" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "user_role_assignments_inventory_users_user" FOREIGN KEY ("user_id") REFERENCES "inventory_users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "userroleassignment_expires_at" to table: "user_role_assignments"
CREATE INDEX "userroleassignment_expires_at" ON "user_role_assignments" ("expires_at");
-- Create index "userroleassignment_role_id" to table: "user_role_assignments"
CREATE INDEX "userroleassignment_role_id" ON "user_role_assignments" ("role_id");
-- Create index "userroleassignment_tenant_id" to table: "user_role_assignments"
CREATE INDEX "userroleassignment_tenant_id" ON "user_role_assignments" ("tenant_id");
-- Create index "userroleassignment_tenant_id_user_id_role_id" to table: "user_role_assignments"
CREATE UNIQUE INDEX "userroleassignment_tenant_id_user_id_role_id" ON "user_role_assignments" ("tenant_id", "user_id", "role_id");
-- Create index "userroleassignment_user_id" to table: "user_role_assignments"
CREATE INDEX "userroleassignment_user_id" ON "user_role_assignments" ("user_id");
-- Create "warranties" table
CREATE TABLE "warranties" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "serial_number" character varying NOT NULL, "customer_id" uuid NULL, "purchase_date" timestamptz NOT NULL, "warranty_start" timestamptz NOT NULL, "warranty_end" timestamptz NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "notes" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "warranties_items_warranties" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "warranty_status" to table: "warranties"
CREATE INDEX "warranty_status" ON "warranties" ("status");
-- Create index "warranty_tenant_id_item_id" to table: "warranties"
CREATE INDEX "warranty_tenant_id_item_id" ON "warranties" ("tenant_id", "item_id");
-- Create index "warranty_tenant_id_serial_number" to table: "warranties"
CREATE INDEX "warranty_tenant_id_serial_number" ON "warranties" ("tenant_id", "serial_number");
-- Create index "warranty_warranty_end" to table: "warranties"
CREATE INDEX "warranty_warranty_end" ON "warranties" ("warranty_end");
