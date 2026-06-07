# Inventory — Sprint: Procurement Breakdowns & P2P Wiring (Retail POS Revamp)

**Created:** 2026-06-07 · **Driver:** `/.claude/plans/_audit-parts/retail-pos-audit-and-roadmap-2026-06-07.md`
**Status:** Planned (Phase 1 + Phase 4)

> Context: the sampled POS systems (jampos "Break Bulk" / "Production Inventory Transfer";
> godigital purchase chain) expose a bulk→retail-unit **breakdown** op and rich procurement UI.
> inventory-api already owns PO/GRN/purchasereturn/requisition/RFQ/stocktransfer/stockadjustment/
> productionbatch/recipe (incl. `recipeingredient.waste_percent`) and **already publishes**
> `inventory.purchase_order.received` + `inventory.goods_receipt.created/posted`. Gaps: a
> first-class breakdown op, a supplier-rebate accrual signal, and procurement/production/asset UI.

## Phase 1 — Breakdown op + P2P signals (backend)
- [ ] **`StockBreakdown`** (new Ent schema): `parent_item_id`, `parent_uom`, `parent_qty`,
  `child_item_id`, `child_uom`, `child_qty`, `conversion_factor`, `cost_allocated`,
  `warehouse_id`, `reason`, `created_by`. Semantics: explode 1 bulk SKU (crate/sack) into N retail
  child units; **conserve total inventory value** by carrying cost parent→child (IAS-2: feeds FIFO
  layers / moving-average). Distinct from BOM production (which transforms inputs→outputs).
  - [ ] `POST /{tenant}/stock/breakdowns` (+ list/get); decrement parent balance, increment child
    balance & cost; write `stock_adjustment` movements both sides.
  - [ ] Publish `inventory.stock.broken_down` `{parent_sku, child_sku, parent_qty, child_qty, cost}`.
  - [ ] Atlas migration; Ent gen; tests for value conservation.
- [ ] **Supplier-rebate accrual signal**: optional `rebate_eligible` + `rebate_rule_id` on
  `purchase_order_line`; on GRN, emit qualifying-purchase volume in `goods_receipt.posted` payload so
  treasury can accrue the rebate (rebate accounting lives in treasury — see treasury sprint-13).
- [ ] Confirm `inventory.goods_receipt.posted` payload carries PO ref + line costs for treasury's
  GR/IR accrual + 3-way match (see treasury sprint-6 R3). Add fields if missing.

## Phase 4 — Procurement / production / asset UI (inventory-ui)
- [ ] PO/LPO list+create, approval, GRN, supplier accounts (read treasury AP `vendor_balance`).
- [ ] RFQ compare/award; requisition→PO chain with quantity-remaining + shipping status.
- [ ] **Production** workflow: ingredients table with Input Qty / **Wastage %** / Final Qty / cost
  (maps to `productionbatch` + `rawmaterialusage` + `recipeingredient.waste_percent`).
- [ ] **Breakdown** UI (parent → children preview, cost split).
- [ ] Asset dashboard (warranty-expiry alerts, staff allocation) over existing `asset*` schemas.
- [ ] Stock reports: valuation (purchase vs sale price → potential profit), Most-Profitable,
  Deadstock; CSV/PDF export.

## Definition of Done
- [ ] `go build ./...`; Ent+Atlas migration applied to local PG17; `pnpm build` (inventory-ui).
- [ ] Breakdown conserves inventory value; events consumed by treasury (GR/IR) verified via logs.

## Use-case page placement (2026-06-07)
Breakdowns + Production live on the **Manufacturing** use-case pages, not the generic Items page. See
`inventory-ui/docs/use-case-pages.md` for the page split (Retail Products vs Catalog/Menu vs
Manufacturing vs Services vs Hospitality-Masters vs Assets). Phase 4 inventory-ui builds these per use case.