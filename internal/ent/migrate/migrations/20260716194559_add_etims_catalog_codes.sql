-- Modify "items" table
ALTER TABLE "items" ADD COLUMN "etims_item_cls_cd" character varying NULL, ADD COLUMN "etims_pkg_unit_cd" character varying NULL, ADD COLUMN "etims_qty_unit_cd" character varying NULL;
-- Modify "units" table
ALTER TABLE "units" ADD COLUMN "kra_qty_unit_cd" character varying NULL;
