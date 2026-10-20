# Backdated stock adjustments (2026-10-20)

A client needed to enter a stock adjustment for a day that has already passed (a count or correction
done earlier, only recorded now) and have it sit on that day in stock history, the Adjustments list
and the books, not on top under today. Stock transfers already do this with `transfer_date`;
adjustments now follow the same idea.

## How it works

- `POST /inventory/adjust` and `POST /inventory/adjustments` take an optional `adjustment_date`
  (`YYYY-MM-DD`). It is stored as `stock_adjustments.adjusted_at`, the effective date. The immutable
  `created_at` keeps the real entry time. No schema change and no migration: both columns existed.
- Rules (`stock/adjustment_date.go`, `ResolveAdjustmentDate`): blank or today means a normal
  adjustment and keeps its real clock time; an earlier day is stored as the start of that day (UTC);
  a future date, a malformed date, or one more than 366 days back is refused with 400
  `INVALID_ADJUSTMENT_DATE` before any stock moves. Unlike a transfer, an adjustment is never
  post-dated, because it changes the live balance immediately.
- A row is "backdated" when `created_at - adjusted_at` exceeds 12 hours (`IsBackdatedAdjustment`).
  A real backdate is always at least 24 hours apart and every other writer stamps both columns
  together; production had 0 of 14,893 rows apart before this shipped, so no legacy row reads as
  backdated.
- The live balance still moves by the change when it is entered. The ledger places the row on its
  date, so the running balance of older rows is unchanged and later rows include it.

## Where the date shows

- **Stock history** (`ItemStockHistory`): ordered by `adjusted_at`, so the row lands among its dated
  neighbours. A backdated row's stored `quantity_after` is the balance at entry time, which is wrong
  in the middle of older rows, so it is not used as an anchor: its "new quantity" is computed from
  its neighbours, and its real entry time is returned as `entered_at` (the page shows an amber
  "entered ..." note, same as a backdated transfer).
- **Stale anchors** (`shiftStaleAnchors`): an adjustment written before a later entry that was dated
  earlier than it holds a snapshot that is short by that entry's change (written 1 Sep at 100, then
  +10 backdated to 15 Aug, now truly 110). The ledger corrects those snapshots per warehouse before
  walking the running balance. This also repairs the same latent gap for backdated transfers.
- **Adjustments list** (`ListAdjustments`) already ordered by `adjusted_at`; the table adds the same
  amber "entered" note.
- **Books**: the valued `stock.adjusted` event carries `adjusted_at` as the effective date. treasury-api
  (`expenses/stock_adjustment_subscriber.go`, `adjustmentEffectiveDate`) dates the write-off expense or
  the inventory value journal on that day; a missing, malformed or future value falls back to now.
- **Approvals**: a gated adjustment keeps its `adjustment_date` in the approval payload and is replayed
  with it (today's date is left out, or a replay days later would read as a backdate).

## UI

inventory-ui adds an "Adjustment date" field (`AdjustmentDateField`, default today, never later than
today, 366 days back) to the Adjustments page modal and the stock drawer's adjust form.

## Not covered

Bulk adjust and stock take postings still stamp the entry time; a backdated stock take is a separate
decision (it would need the same date threaded through the bulk job).
