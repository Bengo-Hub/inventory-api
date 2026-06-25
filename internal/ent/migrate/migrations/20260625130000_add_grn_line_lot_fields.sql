-- Add lot/batch capture columns to "goods_receipt_lines" (lot-tracked / perishable receipts → InventoryLot)
ALTER TABLE "goods_receipt_lines" ADD COLUMN "lot_number" character varying NULL, ADD COLUMN "expiry_date" timestamptz NULL;
