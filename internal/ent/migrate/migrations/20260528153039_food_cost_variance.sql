-- Create "food_cost_variances" table
CREATE TABLE "food_cost_variances" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "period_start" timestamptz NOT NULL, "period_end" timestamptz NOT NULL, "recipe_sku" character varying NOT NULL, "recipe_name" character varying NOT NULL, "theoretical_cost" double precision NOT NULL, "actual_cost" double precision NOT NULL, "variance_pct" double precision NOT NULL, "breakdown" jsonb NULL, "calculated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "foodcostvariance_tenant_id_period_start_period_end" to table: "food_cost_variances"
CREATE INDEX "foodcostvariance_tenant_id_period_start_period_end" ON "food_cost_variances" ("tenant_id", "period_start", "period_end");
-- Create index "foodcostvariance_tenant_id_recipe_sku" to table: "food_cost_variances"
CREATE INDEX "foodcostvariance_tenant_id_recipe_sku" ON "food_cost_variances" ("tenant_id", "recipe_sku");
