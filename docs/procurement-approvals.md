# Procurement approvals: purchase orders and goods receipts

How the approval matrix applies to buying stock, and when a purchase order can still be changed.

## Goods receipts (module `goods_receipt`)

Receiving goods creates a goods receipt (GRN) as a **draft**. Stock only moves when the receipt is **posted**. When an active `goods_receipt` approval rule matches the order amount, posting waits for sign-off.

- **One open receipt per order.** "Mark Received" (`PUT /purchase-orders/{id}/receive`) works on the order's existing draft receipt if there is one. It only opens a new receipt when none is open. The Goods Receipt dialog (`POST /purchase-orders/{id}/goods-receipts`) answers `409 RECEIPT_OPEN` while one is open.
- **Pending approval.** Receiving again while the receipt waits for sign-off answers `409 APPROVAL_PENDING` and creates nothing.
- **Approve posts the receipt.** Approving the request posts the receipt straight away: stock in, cost layers, order line quantities, order status, and the `goods_receipt.posted` event treasury bills from. It happens exactly once: `executed_at` on the approval request is claimed before posting, and the receipt's own draft to posted switch is a second guard. If posting fails the claim is released and "Mark Received" (or the receipt's Post button) finishes it later, since the approval is already in place.
- **Reject cancels the receipt.** The draft is cancelled and the order can be received again, with a new receipt and a new approval request.
- **Never past a closed order.** A receipt is not posted against an order that is already fully received or cancelled (`409 ORDER_CLOSED`).
- **Cancel a draft** with `POST /goods-receipts/{id}/cancel` (Cancel receipt in the receipt drawer). A still-pending approval request for it is withdrawn. Posted receipts are reversed with a purchase return.
- Older data with several drafts on one order answers `409 MULTIPLE_OPEN_RECEIPTS` listing them; cancel the extras and keep one.

The order detail (`GET /purchase-orders/{id}`) returns `open_receipt` (`id`, `grn_number`, `approval_status`, `count`). The screen uses it to show "Awaiting receipt approval" and to hide Mark Received, Amend, Send and Cancel while a receipt waits.

## When can a purchase order be amended?

`PUT /purchase-orders/{id}/amend` replaces the whole order as the form shows it: lines, supplier, warehouse, payment terms, shipping, notes and dates.

| Order status | Amend allowed? | What happens |
|---|---|---|
| draft | Yes | Saved as a draft. |
| sent, nothing received, no open receipt | Yes | Stays sent if the total and supplier are unchanged. If either changes it goes **back to draft**: send it again so the approval (`purchase_order` rule) and the treasury budget check run on the new figures. |
| sent with an open receipt | No, `409 RECEIPT_OPEN` | Post or cancel the receipt first. |
| partially received, received, cancelled | No | Use a purchase return or a new order. |

When the amount changes, any pending or approved `purchase_order` approval request for the old amount is closed (cancelled), so the changed order needs approval again before Send.

Cancelling an order with an open receipt also answers `409 RECEIPT_OPEN`.

## Events

`inventory.purchase_order.amended` carries the budget fields of `purchase_order.sent` (`po_id`, `subtotal`, `total_amount`, `currency`, `project_id`, `order_date`) plus `status`, `previous_status`, `previous_total_amount` and `returned_to_draft`. Treasury refreshes the order's budget commitment to the new total while the status is `sent` and releases it otherwise.

`inventory.goods_receipt.cancelled` is published when a draft receipt is cancelled.

## Other gated modules

Stock adjustments, write-offs and stock counts post when approved (payload replay or variance posting). Transfers, purchase returns, production batches, contracts, RFQ awards and the asset modules are "approve, then press the action again": the gate is keyed on the existing document, so repeating the action after approval goes through.
