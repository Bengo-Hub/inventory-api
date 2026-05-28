-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "total_capacity" bigint NULL, ADD COLUMN "booked_capacity" bigint NULL DEFAULT 0, ADD COLUMN "event_start_at" timestamptz NULL, ADD COLUMN "event_end_at" timestamptz NULL, ADD COLUMN "event_venue" character varying NULL;
