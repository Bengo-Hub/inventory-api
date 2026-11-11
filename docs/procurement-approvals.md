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

## One live approval per document

Submitting a document for approval never opens a second workflow for it. When a pending request already exists for the same tenant, module and document at the same amount, or an approved request whose effect has not run yet (`executed_at` empty), the submit returns that request. The purchase order submit endpoint then answers `already_submitted: true` and no second "submitted" event goes out. If the amount changed, the old live request (pending, or approved and not executed) is cancelled and a new one is created, because the approver signed off on a different figure; an old approval can then never come back into force. Executed requests are history and are not touched. A reused pending request keeps the rule and steps it was created with, even if the rule was edited since; reject it and resubmit to pick up the new steps. A requisition can only be submitted from draft or rejected (400 otherwise), so an approved requisition is never sent round again.

The database backs this up with the partial unique index `approvalrequest_pending_unique` on `approval_requests (tenant_id, module, object_id) WHERE status = 'pending'`. If two submits race, the second insert hits the index and gets the first request back.

The index cannot be built while older duplicates exist, so `cmd/migrate` runs `migrate.PreMigrate` before the schema diff. It keeps the newest pending request per tenant, module and document and cancels the older ones (the same supersede rule Submit always applied). Checking for duplicates by hand:

```sql
SELECT tenant_id, module, object_id, count(*) AS pending, min(created_at), max(created_at)
FROM approval_requests WHERE status = 'pending'
GROUP BY tenant_id, module, object_id HAVING count(*) > 1;
```

## Permanent delete (platform owners only)

Two endpoints replace deleting rows by hand in SQL. Both require the signed `is_platform_owner` claim; a tenant admin or the superuser role gets 403, and the tenant slug is never consulted. Each runs in one transaction and writes an `audit_logs` row with a `before_json` snapshot of what was removed.

`DELETE /{tenant}/inventory/approval-requests/{reqID}` deletes the request and its actions (approval steps belong to rules and are not touched). It answers 409 when:

- the request was executed (`executed_at` set), `APPROVAL_EXECUTED`;
- the request is for a goods receipt that is posted, `OBJECT_POSTED`;
- the request is approved and is not a goods receipt request, `APPROVAL_APPLIED`, because an approved request elsewhere may already have been acted on.

For a goods receipt request, `?with_object=true` also deletes the receipt, its lines and every other approval request for that receipt, but only while the receipt is a draft (`RECEIPT_NOT_DRAFT` otherwise) and nothing else points at it (no lots, serials, pending price changes or purchase returns). The audit action is `approval.hard_deleted`.

`DELETE /{tenant}/inventory/purchase-orders/{poID}?hard=true` deletes a purchase order, its lines, its draft and cancelled receipts with their lines, every approval request for the order and those receipts, and its contract links, and clears the order from RFQ awards. It answers 409 `RECEIPT_POSTED` when stock came in against the order: a posted receipt, a received or partially received status, or any line with a received quantity (older orders were partly received and then cancelled before goods receipts existed). It also answers 409 when a purchase return exists (`HAS_PURCHASE_RETURN`) or when one of its approvals was executed. Inventory cannot see treasury bills; bills come from received stock, which these rules cover. The delete queues `inventory.purchase_order.cancelled` with ids only (`tenant_id`, `po_id`, `status`, `hard_deleted`, `deleted_at`), so treasury releases any budget commitment. The audit action is `purchase_order.hard_deleted`, and its snapshot lists each line as item id, quantity and unit price. Both endpoints accept an optional `?reason=` that is kept on the audit row. Without `hard=true` the endpoint answers 400; cancel is the normal way to stop an order.

Each guard is checked twice. The handler reads it first to give a clear message, and then every delete statement carries the same condition (request not executed, receipt still draft or cancelled, order not received, line with nothing received) and compares the number of rows it removed. If an approval or a post commits between the read and the delete, the count comes up short and the whole transaction rolls back with a 409.

In the app, "Delete permanently" appears on the approval detail and on the purchase order drawer only for platform owners, behind a confirmation that says it cannot be undone.
