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
   `to_buy_cost`, `po_ids`, `po_numbers`, `unresolved_lines` and `to_buy_lines` (per line: item,
   quantity still to buy, unit cost). treasury shows it on the invoice and pre-fills purchases.

## Buying directly

When the business buys the goods itself and pays from a bank (treasury: Buy goods for this job),
treasury publishes `treasury.job_goods_purchased` with the root, the purchase (expense) id and the
lines bought. The consumer receives exactly those quantities into stock, once per purchase
(reference `sale-<root>:bought-<purchase>`, reason `transfer_in` so the movement is never posted to
the ledger a second time; treasury already debited Inventory through the purchase), then reduces
the sale's draft purchase orders by what was bought: emptied lines are deleted and an order left
with nothing is cancelled. Issued or received orders are real commitments and are left alone.

## Receiving

`inventory.goods_receipt.posted` carries `sales_document_id` (the purchase order's root) so the
vendor bill treasury creates for the receipt counts as goods bought for that job.

## Stock follows the sale (`consumers/sale_goods_events.go`)

treasury decides when a sale's goods leave stock (tenant policy: with the invoice, the default, or
only on delivery; see treasury `docs/general-ledger.md` 5i) and publishes the whole sale as one
snapshot, `treasury.sale_goods_issued`, whenever one of its documents changes: every invoice,
delivery note and credit note with the quantities it should have moved (positive out, negative
back in). The consumer moves each document's stock to that target:

| Reference | Document |
| --- | --- |
| `sale-<root>:inv-<invoice>` | invoice (out) |
| `sale-<root>:dn-<note>` | dispatched delivery note (out) |
| `sale-<root>:cn-<credit note>` | credit note (back in) |
| `sale-<root>:bought-<purchase>` | goods bought directly for the job (in; never touched by a sync) |

- The change per document and item is target minus what its reference already moved, so a repeat
  snapshot moves nothing, an edit moves the difference and a void or cancel brings goods back.
  Snapshots are complete, so they converge whatever order documents change in; a document missing
  from the snapshot (deleted) is brought back to zero.
- Reasons `transfer_out` (out) and `return` (back): never posted to the ledger from inventory,
  treasury posts the sale's cost of sales and stock relief itself.
- The stock goes out of (and back into) the selling outlet's warehouse, else the default one;
  reversals return to the warehouse the reference used.
- Negative stock is allowed (oversell debt): goods sold before their purchase order is received go
  negative and the goods receipt brings them back. The stock page's "Below zero" filter
  (`GET /stock?negative=true`, also in the export) lists them.

The earlier delivery-note consumer (`treasury.delivery_note.dispatched` deducting stock itself) is
removed: treasury now includes dispatched notes in the sale snapshot.

Historical invoices were not back-filled: codevertex's two goods jobs were bought and delivered
directly and never held in inventory, so a stock-out now would only create negative stock.
