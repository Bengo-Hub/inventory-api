-- Create "consumptions" table
CREATE TABLE "consumptions" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "order_id" uuid NOT NULL, "warehouse_id" uuid NULL, "items" jsonb NOT NULL, "reason" character varying NOT NULL DEFAULT 'sale', "status" character varying NOT NULL DEFAULT 'processed', "idempotency_key" character varying NULL, "processed_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "consumption_idempotency_key" to table: "consumptions"
CREATE UNIQUE INDEX "consumption_idempotency_key" ON "consumptions" ("idempotency_key");
-- Create index "consumption_tenant_id_order_id" to table: "consumptions"
CREATE INDEX "consumption_tenant_id_order_id" ON "consumptions" ("tenant_id", "order_id");
-- Create "tenants" table
CREATE TABLE "tenants" ("id" uuid NOT NULL, "name" character varying NOT NULL, "slug" character varying NOT NULL, "status" character varying NOT NULL DEFAULT 'active', "contact_email" character varying NULL, "contact_phone" character varying NULL, "logo_url" character varying NULL, "website" character varying NULL, "country" character varying NULL DEFAULT 'KE', "timezone" character varying NULL DEFAULT 'Africa/Nairobi', "brand_colors" jsonb NULL, "org_size" character varying NULL, "use_case" character varying NULL, "subscription_plan" character varying NULL, "subscription_status" character varying NULL, "subscription_expires_at" timestamptz NULL, "subscription_id" character varying NULL, "tier_limits" jsonb NULL, "metadata" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "tenant_slug" to table: "tenants"
CREATE UNIQUE INDEX "tenant_slug" ON "tenants" ("slug");
-- Create index "tenant_status" to table: "tenants"
CREATE INDEX "tenant_status" ON "tenants" ("status");
-- Create index "tenants_slug_key" to table: "tenants"
CREATE UNIQUE INDEX "tenants_slug_key" ON "tenants" ("slug");
-- Create "items" table
CREATE TABLE "items" ("id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "description" text NULL, "category" character varying NULL, "price" double precision NOT NULL DEFAULT 0, "unit_of_measure" character varying NOT NULL DEFAULT 'PIECE', "is_active" boolean NOT NULL DEFAULT true, "image_url" character varying NULL, "metadata" jsonb NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "items_tenants_items" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "item_tenant_id_category" to table: "items"
CREATE INDEX "item_tenant_id_category" ON "items" ("tenant_id", "category");
-- Create index "item_tenant_id_is_active" to table: "items"
CREATE INDEX "item_tenant_id_is_active" ON "items" ("tenant_id", "is_active");
-- Create index "item_tenant_id_sku" to table: "items"
CREATE UNIQUE INDEX "item_tenant_id_sku" ON "items" ("tenant_id", "sku");
-- Create "warehouses" table
CREATE TABLE "warehouses" ("id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "address" text NULL, "is_default" boolean NOT NULL DEFAULT false, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "warehouses_tenants_warehouses" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "warehouse_tenant_id_code" to table: "warehouses"
CREATE UNIQUE INDEX "warehouse_tenant_id_code" ON "warehouses" ("tenant_id", "code");
-- Create index "warehouse_tenant_id_is_active" to table: "warehouses"
CREATE INDEX "warehouse_tenant_id_is_active" ON "warehouses" ("tenant_id", "is_active");
-- Create index "warehouse_tenant_id_is_default" to table: "warehouses"
CREATE INDEX "warehouse_tenant_id_is_default" ON "warehouses" ("tenant_id", "is_default");
-- Create "inventory_balances" table
CREATE TABLE "inventory_balances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "on_hand" bigint NOT NULL DEFAULT 0, "available" bigint NOT NULL DEFAULT 0, "reserved" bigint NOT NULL DEFAULT 0, "unit_of_measure" character varying NOT NULL DEFAULT 'PIECE', "updated_at" timestamptz NOT NULL, "item_id" uuid NOT NULL, "warehouse_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "inventory_balances_items_balances" FOREIGN KEY ("item_id") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "inventory_balances_warehouses_balances" FOREIGN KEY ("warehouse_id") REFERENCES "warehouses" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "inventorybalance_tenant_id_item_id" to table: "inventory_balances"
CREATE INDEX "inventorybalance_tenant_id_item_id" ON "inventory_balances" ("tenant_id", "item_id");
-- Create index "inventorybalance_tenant_id_item_id_warehouse_id" to table: "inventory_balances"
CREATE UNIQUE INDEX "inventorybalance_tenant_id_item_id_warehouse_id" ON "inventory_balances" ("tenant_id", "item_id", "warehouse_id");
-- Create "recipes" table
CREATE TABLE "recipes" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "sku" character varying NOT NULL, "name" character varying NOT NULL, "output_qty" double precision NOT NULL DEFAULT 1, "unit_of_measure" character varying NOT NULL DEFAULT 'PORTION', "is_active" boolean NOT NULL DEFAULT true, "metadata" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
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
