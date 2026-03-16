-- Modify "recipes" table
ALTER TABLE "recipes" ADD COLUMN "prep_time_minutes" bigint NULL;
-- Create "units" table
CREATE TABLE "units" ("id" uuid NOT NULL, "name" character varying NOT NULL, "abbreviation" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "item_units" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "units_items_units" FOREIGN KEY ("item_units") REFERENCES "items" ("id") ON UPDATE NO ACTION ON DELETE SET NULL);
-- Create index "unit_name" to table: "units"
CREATE UNIQUE INDEX "unit_name" ON "units" ("name");
-- Create index "units_name_key" to table: "units"
CREATE UNIQUE INDEX "units_name_key" ON "units" ("name");
