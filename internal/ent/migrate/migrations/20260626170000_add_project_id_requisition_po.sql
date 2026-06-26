-- Link requisitions + purchase orders to a projects-service project (cost attribution).
ALTER TABLE "requisitions" ADD COLUMN "project_id" uuid NULL;
ALTER TABLE "purchase_orders" ADD COLUMN "project_id" uuid NULL;
