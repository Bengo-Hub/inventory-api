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
  - Deadstock and Fast-Moving follow the location (2026-11-02). The top-nav outlet narrows them to
    the warehouses that outlet owns (not the shared outlet-less ones, which would count a central
    store at every outlet), and `?warehouse_id=` picks one location, which must belong to the
    selected outlet. Stock and movement both come from the scoped warehouses, so an item selling at
    one location is still dead stock where it sits idle. For a user `OutletContext` marks as
    restricted, the report ignores `?outlet_id=` and keeps their own outlet. The PDF subtitle
    names the location. Both reports now aggregate movement in Postgres
    (`items/stock_movement.go`, consumption lines unnested and grouped per SKU) instead of loading
    every consumption into memory, backed by the new `consumption_tenant_id_created_at` index
    (migration `20261102204401`). The Deadstock page has the same location picker as Stock
    Valuation.
  - Fast Moving page (2026-11-03): `/reports/fast-moving` in inventory-ui (Stock reports menu), same
    location picker as Deadstock (shared `components/reports/report-location-scope.tsx`), windows of
    7 to 90 days, units moved and per day, busy items already out of stock, and a PDF/CSV export
    (`GET .../inventory/reports/fast-moving.pdf`).
  - Outlet context after authentication (2026-11-03, was open): `OutletContext` and
    `EnforceOutletAssignment` used to run at the `/{tenant}` level before any auth middleware, so
    they never saw claims and every caller's `X-Outlet-ID` was taken as is. They now run inside each
    authenticated group (`outletScope` in the router: private routes, the inventory tree and the
    `/v1` service tree). Aligned with pos-api: admins and the manager tier work in any outlet; other
    users' outlet header counts only for an outlet they are assigned to (a user with no assignment
    yet is not restricted); otherwise the header is dropped and the outlet in their token applies.
    Anonymous catalogue reads still pick the storefront outlet from the header.
  - Incident the same day (fixed in `9a6370c`): the first version answered 403
    `OUTLET_NOT_ASSIGNED` for an unassigned outlet. inventory-ui sends the top-nav outlet on every
    request, `/auth/me` included, so branch users (the codevertex-demo personas) were stuck at
    initialising. Dropping the header keeps the restriction without blocking the app.
  - The `GET /inventory/recipes` 403s seen the same day were the same outlet rejection, not the
    recipes use-case gate (boi-enterprises' outlets are retail and hospitality, both allowed); none
    were logged after `9a6370c`. inventory-ui already skips recipes for pharmacy and services
    outlets, so nothing needed changing there.
  - Second incident the same day (BOI, "the quantity of products in all outlets appears the same
    on POS"): moving `OutletContext` after auth made its service-account branch reachable for the
    first time, and that branch passed through without reading `X-Outlet-ID`. pos-api reads each
    outlet's `on_hand` on `/v1` with its service key and the outlet header, so every outlet got
    the tenant-wide quantity (item 10289: 4 at Junior Wholesalers, whose balance is 0). Service
    calls now keep the outlet they name, unrestricted; without a header they stay tenant-wide.
    `TestOutletContextRules` covers both. Any S2S caller relying on `X-Outlet-ID` was affected
    from `f5a4074` until this fix.

## Definition of Done
- [ ] `go build ./...`; Ent+Atlas migration applied to local PG17; `pnpm build` (inventory-ui).
- [ ] Breakdown conserves inventory value; events consumed by treasury (GR/IR) verified via logs.

## Use-case page placement (2026-06-07)
Breakdowns + Production live on the **Manufacturing** use-case pages, not the generic Items page. See
`inventory-ui/docs/use-case-pages.md` for the page split (Retail Products vs Catalog/Menu vs
Manufacturing vs Services vs Hospitality-Masters vs Assets). Phase 4 inventory-ui builds these per use case.