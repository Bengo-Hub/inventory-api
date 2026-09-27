# Procure for the job

A business has to buy the goods it sells before it can supply them. The books are only right when
that purchase is recorded: money leaves the bank when the goods are bought, and becomes cost of
goods sold when they are sold. This service turns a committed sale into purchase orders for the
goods the business does not already hold.

## Trigger

treasury publishes `treasury.goods_committed` once per sale, the moment the business commits to
supplying goods:

- a quotation is accepted,
- a sales order is confirmed (sent),
- an invoice is issued without an earlier commitment.

Every document of one sale shares a root: the first document of the chain (the quotation, else the
sales order, else the invoice). The payload carries `root_id`, `root_number`, `source_type`,
`invoice_id`, `outlet_id`, `currency`, `customer_name` and the goods lines (`item_id`, `sku`,
`description`, `quantity`, `unit_cost`). Service and voucher lines are never sent.

## What the consumer does (`consumers/goods_committed_events.go`)

1. Claims `procure-to-order:<root>` in `idempotency_keys`; a sale is procured once. A root that
   already holds the older `quotation-accepted:<root>` key is treated as done.
2. Resolves each line to an item (id, then sku). SERVICE and VOUCHER items are skipped.
3. Allocates the available stock (sum of `inventory_balances.available` across warehouses) to the
   lines in order, so two lines of one item share its stock. Only the rest is bought. A goods line
   that resolves to no item is counted as goods to buy in full, but gets no purchase order line.
4. Cuts one draft purchase order per preferred supplier for the shortfall, at the line's buying
   cost (treasury's snapshot, else the item cost). `quotation_id` / `quotation_number` hold the
   sale's root document. The receiving warehouse is the selling outlet's, else the default.
5. Emits `inventory.procure_to_order.evaluated` with `goods_cost`, `from_stock_cost`,
   `to_buy_cost`, `po_ids`, `po_numbers` and `unresolved_lines`. treasury shows it on the invoice.

## Buying directly

When the business buys the goods itself and pays from a bank (treasury: Buy goods for this job),
treasury publishes `treasury.job_goods_purchased` with the root. The consumer cancels that sale's
purchase orders that are still draft. Issued or received orders are real commitments and are left
alone.

## Receiving

`inventory.goods_receipt.posted` carries `sales_document_id` (the purchase order's root) so the
vendor bill treasury creates for the receipt counts as goods bought for that job.

## Known gap

Goods invoiced without a delivery note are expensed in the ledger but never leave stock here: stock
only drops when a delivery note is dispatched (`treasury.delivery_note.dispatched`).
