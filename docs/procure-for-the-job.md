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
treasury publishes `treasury.job_goods_purchased` with the root. The consumer receives the
quantities of the sale's still-draft purchase orders into stock (reference
`sale-<root>:bought-<po>`, once per order) and cancels those drafts. Issued or received orders are
real commitments and are left alone.

## Receiving

`inventory.goods_receipt.posted` carries `sales_document_id` (the purchase order's root) so the
vendor bill treasury creates for the receipt counts as goods bought for that job.

## Goods leave stock once per sale (`consumers/sale_goods_events.go`)

treasury expenses an invoice's goods when it is issued, so stock leaves at the same moment, with or
without a delivery note. Every stock-out of a sale carries a reference starting `sale-<root>:`:

| Reference | Written by |
| --- | --- |
| `sale-<root>:inv-<invoice>` | `treasury.sale_goods_issued` (invoice sent / voided) |
| `sale-<root>:dn-<note>` | `treasury.delivery_note.dispatched` (now carries `root_id`) |
| `sale-<root>:bought-<po>` | goods bought directly for the job (stock in) |

- On every invoice send (`action: issue`) the invoice's stock-outs are synced to its quantities
  less what the sale's delivery notes already took (a note dispatched from a sales order before
  invoicing). A resend moves nothing; a re-issue after an edit moves only the difference.
- On void (`action: reverse`) everything the invoice took goes back to stock (reason `return`).
- A delivery note dispatched after an invoice of the same sale has issued the goods moves no stock:
  it is logistics only. Delivery notes without a root keep the older reference and behaviour.
- Negative stock is allowed (oversell debt): goods invoiced before their purchase order is received
  go negative and the goods receipt brings them back.

Historical invoices were not back-filled: codevertex's two goods jobs were bought and delivered
directly and never held in inventory, so a stock-out now would only create negative stock.
