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

When the amount or the supplier changes, any pending `purchase_order` approval request, or an approved one not yet used to send the order, is closed (cancelled), so the changed order needs approval again before Send. An approval that was already used to send the order stays as history.

Cancelling an order with an open receipt also answers `409 RECEIPT_OPEN`.

## Events

`inventory.purchase_order.amended` carries the budget fields of `purchase_order.sent` (`po_id`, `subtotal`, `total_amount`, `currency`, `project_id`, `order_date`) plus `status`, `previous_status`, `previous_total_amount` and `returned_to_draft`. Treasury refreshes the order's budget commitment to the new total while the status is `sent` and releases it otherwise.

`inventory.goods_receipt.cancelled` is published when a draft receipt is cancelled.

## Every gated module: reject, fix, resubmit, approve once

The same two rules hold in every module under the approval matrix.

**A rejected document can be fixed and resubmitted.** Rejecting a request never locks the document. It stays editable where the module allows editing, and pressing the action again opens a new approval request. The rejected request stays as history. The answer is `409` (`422` for stock counts and adjustments) with `approval_required: true` and `state`: `submitted` for the first request, `resubmitted` for a new one after a rejection, `pending` while a request waits. The same happens when the document changed since it was approved (a different amount, or for RFQ awards a different selection): the old approval is cancelled and a new request opens, so an approval only ever covers the version it was given for.

**Approving runs the action, exactly once.** The moment the last step approves, the action runs. Before acting, `executed_at` on the request is claimed (only one caller can set it), and every action also switches its document with a conditional update (draft to shipped, pending to paid, planned to in progress), so a second run finds nothing to do and stock or money never moves twice. If the action fails, the claim is released and the request stays "approved, not executed". Pressing the action again then runs it on that same approval, and so does `POST /approval-requests/{id}/execute` (needs the approvals change permission). Once executed, an approval is spent: doing the action again later needs a new approval.

| Module | Action the approval runs | Fix after a rejection | Notes |
|---|---|---|---|
| `stock_transfer` | Ship (stock leaves the source warehouse) | Amend the draft transfer, press Ship | Amending withdraws an approval that has not shipped yet, so new quantities are approved again. |
| `stock_count` | Post the variances and close the count | Correct the count lines, press Approve | The amount is the total variance, so a correction is a new request. |
| `purchase_return` | Take the goods out of stock, publish the credit (or repair) event | Press Approve again | All or nothing, as before. |
| `production_batch` | Start the batch (issue raw materials) | Press Start again | The material check runs before the request opens. A forced start (`?force=true`) is stored on the request and is a separate decision from a normal start. |
| `contract` | Activate | Edit the contract, press Activate | |
| `rfq` | Write the award, mark the RFQ awarded, raise draft contracts | Change the selection, press Award | The award selection is stored on the request and applied on approval. |
| `asset_maintenance`, `asset_transfer`, `asset_disposal` | Complete the job (a disposal also publishes `inventory.asset.disposed` once) | Press Complete again | An asset transfer must still pass its own approval step first. |
| `goods_receipt` | Post the receipt | A rejected receipt is cancelled; receive again | See the goods receipt section above. |
| `stock_adjustment`, `stock_writeoff` | Post the stored adjustment | Enter the adjustment again (each entry is its own request) | |
| `purchase_order` | Nothing runs by itself | Amend the draft order, Submit for approval | Staff press Send to supplier. Send runs the treasury budget check with the sender's override rights and tells the supplier, so it is not done unattended. The order shows "Approved, ready to send" until then, and Send marks the approval executed. |
| `requisition` | The requisition is approved and raises its orders | Edit, submit again | |

**The gate fails closed.** When the approval check cannot be completed (the rule lookup, the request lookup or the request write fails), the action does not run and the answer is `503 APPROVAL_UNAVAILABLE`. Only "no rule matches" lets an action through without approval. This covers every gated action, stock adjustments and Send to supplier included.

**What counts as the same submission.** A request is matched on the amount, plus the stored inputs where a module has them (RFQ award selection, forced batch start). Two consequences we accept: an edit that keeps the amount (a contract edit with the same value, a purchase return edit with the same total, a stock count correction with the same total variance) keeps its approval; and amending a transfer withdraws its approval even for a notes-only edit, which costs one extra sign-off.

**Stock counts.** The amount is the total variance of the whole count, posted lines included. If an approved count posts only some lines (an item cannot be adjusted), the claim is released and the same approval stays valid: fix the item and press Approve again, or use Execute, and the remaining lines post on the original approval. A cancelled count never posts: cancelling it withdraws its live request, and an approval for a count that is cancelled anyway is refused. Changing any count line withdraws the count's live request (pending, or approved and not run yet), even when the total variance stays the same, so the next Approve opens a fresh request for the numbers the count now has; re-saving a line unchanged keeps it. A posted line cannot be edited (`409 LINE_POSTED`), lines are frozen while the approval is being carried out (`409 COUNT_POSTING`), and a count that has posted anything cannot be cancelled (`409 COUNT_POSTED`). A line change locks the count's approval requests (`SELECT ... FOR UPDATE`), withdraws the live one and writes the line in one transaction, so a claim starting at the same moment waits and then finds the approval withdrawn. As a second guard, posting on an approval (the executor, Execute or Approve) first checks that the count's total variance still equals the approved amount, and otherwise refuses with `409 COUNT_CHANGED` and posts nothing. A stock count approval stored before this check may hold a total rounded differently and is then refused the same way; submitting the count for approval again fixes it.

**Running a failed approval.** Approvers with the approvals change permission see "Execute approved action" on an approved request whose action has not run; it calls `/execute`, which writes an `approval.executed_manually` audit row with the caller. The document keeps the approver who signed it off. Pressing the action on the document itself also runs it on the same approval.

Requests opened before this change have no stored inputs. An old approved RFQ request cannot be replayed (`NO_AWARD_SELECTION`): award again. An old production batch request is replaced on the next Start, because the forced or normal choice is now part of the request.

### Approved but never executed

To list approved requests whose action never ran, per tenant and module, with the document's current state (read only):

```sql
SELECT r.tenant_id, t.slug AS tenant, r.module, r.object_reference, r.object_id, r.amount,
       r.decided_at,
       (r.payload IS NOT NULL AND r.payload::text NOT IN ('null', '{}')) AS has_payload,
       COALESCE(st.status::text, pr.payment_status::text, pb.status::text, c.status::text, q.status::text,
                am.status::text, at.status::text, ad.status::text, sc.status::text, g.status::text,
                po.status::text) AS document_status
FROM approval_requests r
LEFT JOIN tenants t            ON t.id = r.tenant_id
LEFT JOIN stock_transfers st   ON r.module = 'stock_transfer'    AND st.id = r.object_id AND st.tenant_id = r.tenant_id
LEFT JOIN purchase_returns pr  ON r.module = 'purchase_return'   AND pr.id = r.object_id AND pr.tenant_id = r.tenant_id
LEFT JOIN production_batches pb ON r.module = 'production_batch' AND pb.id = r.object_id AND pb.tenant_id = r.tenant_id
LEFT JOIN contracts c          ON r.module = 'contract'          AND c.id = r.object_id  AND c.tenant_id = r.tenant_id
LEFT JOIN rf_qs q              ON r.module = 'rfq'               AND q.id = r.object_id  AND q.tenant_id = r.tenant_id
LEFT JOIN asset_maintenances am ON r.module = 'asset_maintenance' AND am.id = r.object_id AND am.tenant_id = r.tenant_id
LEFT JOIN asset_transfers at   ON r.module = 'asset_transfer'    AND at.id = r.object_id AND at.tenant_id = r.tenant_id
LEFT JOIN asset_disposals ad   ON r.module = 'asset_disposal'    AND ad.id = r.object_id AND ad.tenant_id = r.tenant_id
LEFT JOIN stock_counts sc      ON r.module = 'stock_count'       AND sc.id = r.object_id AND sc.tenant_id = r.tenant_id
LEFT JOIN goods_receipts g     ON r.module = 'goods_receipt'     AND g.id = r.object_id  AND g.tenant_id = r.tenant_id
LEFT JOIN purchase_orders po   ON r.module = 'purchase_order'    AND po.id = r.object_id AND po.tenant_id = r.tenant_id
WHERE r.status = 'approved' AND r.executed_at IS NULL
ORDER BY r.module, t.slug, r.decided_at;
```

Nothing runs these automatically after a deploy, because an old approval may no longer match reality. For each row: when the document already shows the action done (a transfer `in_transit` or `received`, a return `paid`, an order `sent`), the request is history from before the claim existed and needs nothing. Otherwise review it and press the action, or call `/execute`, or, when it should not happen any more, have a platform owner cancel it (below). A stock adjustment row with `has_payload = false` predates stored adjustments and cannot be replayed: enter the adjustment again if it is still wanted.

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

`POST /{tenant}/inventory/approval-requests/{reqID}/cancel` cancels an approved request whose action never ran (`executed_at` empty). Same platform-owner rule as the deletes. The request becomes cancelled and stays as history; nothing is posted, and it can never be executed afterwards (execution only claims approved requests). It answers 409 `APPROVAL_EXECUTED` when the action already ran (also when the executor claims it at the same moment) and 409 `NOT_APPROVED` for any other status. It writes an `approval.cancelled` audit row with the request as it was (payload included) and the optional `?reason=`, and queues `inventory.approval.cancelled`.

In the app, "Cancel approval" appears on the approval detail for platform owners when the request is approved and its action has not run. "Delete permanently" appears on the approval detail and on the purchase order drawer only for platform owners, behind a confirmation that says it cannot be undone.
