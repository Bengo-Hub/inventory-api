-- Modify "suppliers" table
ALTER TABLE "suppliers" ADD COLUMN "auto_pay_enabled" boolean NOT NULL DEFAULT false, ADD COLUMN "payment_terms_days" bigint NOT NULL DEFAULT 0;
