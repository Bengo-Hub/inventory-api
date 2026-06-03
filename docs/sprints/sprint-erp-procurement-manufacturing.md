# Sprint: Absorb ERP Procurement + Manufacturing — inventory-api

**Created:** 2026-06-03
**Status:** 🚧 In progress — **data model + Atlas migration complete (2026-06-03)**; handlers/routes/events/RBAC/swagger + inventory-ui pending before ERP-side deletion.

### Implementation status 2026-06-03 (local commits, unpushed)
**Backends implemented + `go build`/`vet` clean:**
- Procurement (PROC-MIG ✅ entities): requisitions (CRUD + submit/review/approve/reject + convert-to-PO), contracts (+activate/terminate/link), purchase-returns (+approve → stock-out), supplier-performance (list/record), service-delivery, procurement dashboard. Commits `0bade62`,`ded94c7`,`8169771`.
- Manufacturing (MFG-MIG ✅): production batch lifecycle (create/start/complete/cancel), recipe BOM explosion, QC (+auto-fail), dashboard. Stock effects applied in-process (start→consume raw materials, complete→finished-goods receipt). Commit `79d8910`,`0b9d02d`.
- Documents: `internal/modules/documents` — DocumentSequence (per-tenant atomic numbering) + branded PO PDF (go-pdf/fpdf, branding from auth cache) + `GET /inventory/purchase-orders/{id}/pdf`. Commit `b3fe809`.
- Fixed-asset register (inventory owns; depreciation→treasury): 8 schemas + handlers (assets/categories CRUD, maintenance/transfer/disposal/insurance/audit/reservation, `depreciation-run`→`inventory.asset.depreciation_due`). Commit `073267e`.
- Atlas migrations generated for all (procurement/manufacturing, document_sequence, asset_register). swagger regenerated (`0b9d02d`).

**Still pending (not production-complete):**
- [ ] Domain-specific RBAC permissions (currently reuse generic `items.*`).
- [ ] GRN (distinct goods-receipt + 3-way match) and RFQ/sourcing.
- [ ] OpenAPI annotations for the new endpoints (swag regen runs but new routes are undocumented until annotated).
- [ ] Handler/integration tests (INV-ERP-17).
- [ ] erd.md/integrations.md/architecture.md detail for the new entities.
- [ ] inventory-ui pages (procurement/manufacturing/assets).
- [ ] treasury: consume `inventory.asset.depreciation_due` → FixedAssetDepreciation + GL; emit back a snapshot event for inventory to update accumulated_depreciation/book_value.

### Progress 2026-06-03 (commit af925ac, local)
- ✅ Ent schemas added (compile + `go build` clean): `requisition`(+line), `contract`(+order_link), `purchase_return`(+line), `supplier_performance`, `service_delivery` (Procurement); `production_batch`, `batch_raw_material`, `quality_check` (Manufacturing).
- ✅ Atlas migration `internal/ent/migrate/migrations/20260603162054_add_procurement_manufacturing.sql` (11 tables + indexes/FKs), generated via the documented PG17 diff workflow.
- ⏳ Next: module services + HTTP handlers + route registration (app.go/router), RBAC perms, NATS events (PROC-MIG-08 / MFG-MIG-05), `swag init`, inventory-ui screens; then ERP-side removal.
**Goal:** Make inventory-service the single owner of Procurement and Manufacturing so the ERP `procurement/*` and `manufacturing/*` Django apps (and `ecommerce/product`+`stockinventory`+`vendor`) can be fully deleted with no inventory/procurement/manufacturing data left in the ERP.

## Context & decision (2026-06-03)

The ERP is being decomposed. Inventory is fully owned by inventory-service. Audit found:
- ERP `procurement` already partially exists in inventory-service (purchase orders + suppliers) → **move the entire procurement workflow to inventory-service**; remove from ERP.
- ERP `manufacturing` data is ~90% inventory-owned (BOM=recipe, raw materials/finished goods=stock, units) with only `User` (auth) and `Branch` (outlet) as external refs → **inventory-service is the best home**; remove from ERP.

The ERP will retain **no** procurement/manufacturing/inventory workflows or data, and **no** reference-ID shims (per decision: decisive full delete).

## Equivalence already covered in inventory-service

| ERP entity | inventory-service equivalent | Status |
|---|---|---|
| `product.Products` / `Category` / `ProductImages` | `item` / `itemcategory` / `itemasset` | ✅ exists |
| `stockinventory.Variations` / `Unit` / `Warranties` | `itemvariant` / `unit` / `warranty` | ✅ exists |
| `stockinventory.StockInventory` / `StockTransfer` / `StockAdjustment` / `StockTransaction` | `inventorybalance` / `stocktransfer` / `stockadjustment` / `consumption` | ✅ exists |
| `vendor.Vendor` | `supplier` | ✅ exists |
| `procurement.purchases.PurchaseOrder` / lines | `purchaseorder` / `purchaseorderline` | ✅ exists |
| `manufacturing.ProductFormula` / `FormulaIngredient` | `recipe` / `recipeingredient` | ✅ exists |
| `manufacturing.RawMaterialUsage` / `BatchRawMaterial` | `consumption` | ✅ exists |
| `manufacturing.ManufacturingAnalytics` (cost variance) | `foodcostvariance` | ✅ partial |

## Gaps to implement (entities + handlers + events + Atlas migrations + swagger)

### Procurement (PROC-MIG)
- [ ] **PROC-MIG-01:** `requisition` + `requisition_line` (ERP `procurement.requisitions.ProcurementRequest` / `RequestItem`) — request → approval → PO conversion.
- [ ] **PROC-MIG-02:** `contract` + `contract_order_link` (ERP `procurement.contracts.Contract` / `ContractOrderLink`).
- [ ] **PROC-MIG-03:** `purchase_return` + `purchase_returned_item` (ERP `procurement.purchases.PurchaseReturn` / `PurchaseReturnedItem`) — supplier RMA.
- [ ] **PROC-MIG-04:** `supplier_performance` (ERP `procurement.supplier_performance.SupplierPerformance`) — rating/scorecard on existing `supplier`.
- [ ] **PROC-MIG-05:** `service_delivery` (ERP `procurement.requisitions.ServiceDelivery`) — services receipt.
- [ ] **PROC-MIG-06:** `purchase_order_payment` linkage → reference treasury-api payment intents (event/S2S), not a local finance copy.
- [ ] **PROC-MIG-07:** Procurement analytics endpoints (parity with ERP `procurement/analytics`).
- [ ] **PROC-MIG-08:** Events: `inventory.purchase_order.created/received`, `inventory.requisition.approved`, `inventory.goods.received` (→ treasury vendor bill, → stock receipt already internal).

### Manufacturing (MFG-MIG)
- [ ] **MFG-MIG-01:** `production_batch` (work order) — ERP `manufacturing.ProductionBatch` (formula→batch, qty, branch/outlet, supervisor, status FSM).
- [ ] **MFG-MIG-02:** `batch_raw_material` — consumption of raw materials per batch (links to `consumption`/stock backflush).
- [ ] **MFG-MIG-03:** `quality_check` — ERP `manufacturing.QualityCheck` (per batch, inspector, result).
- [ ] **MFG-MIG-04:** Confirm `recipe`/`recipeingredient` cover `ProductFormula`/`FormulaIngredient` (output unit, yield, costing); extend if needed.
- [ ] **MFG-MIG-05:** Finished-goods receipt on batch completion → stock increment (internal) + `inventory.production.completed` event.
- [ ] **MFG-MIG-06:** Manufacturing analytics (extend `foodcostvariance`): batch cost, yield variance.

### Cross-cutting
- [ ] **XMIG-01:** RBAC permissions/roles for procurement + manufacturing in inventory-service.
- [ ] **XMIG-02:** `go generate ./internal/ent` + Atlas diff migrations (never hand-edit), `swag init`, `go build/test`.
- [ ] **XMIG-03:** inventory-ui screens for procurement + manufacturing (or confirm coverage).
- [ ] **XMIG-04:** Update `plan.md`, `erd.md`, `integrations.md`, `architecture.md`.

## ERP removal (after the above are production-ready)

Delete Django apps + migrations + urls + INSTALLED_APPS entries and all importers:
- `ecommerce/product`, `ecommerce/stockinventory`, `ecommerce/vendor`
- `procurement/*` (analytics, contracts, orders, purchases, requisitions, services, supplier_performance)
- `manufacturing/*`
- Strip inventory/product/stock coupling from **kept** modules: `core` (models/tasks/views/analytics), `core_orders/utils`, `hrm/employees/views`. (finance/crm/ecommerce.order/pos couplings are removed with those domains.)
- ERP-UI: remove inventory/products/procurement/manufacturing routes, views, services, menu; add external links to inventory-ui.

## References
- [Ownership map] ../../../../shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md
- [ERP Module Removal Plan](../../../../erp/erp-api/docs/module-removal-plan.md)
- [inventory-api ERP gaps](./sprint-erp-gaps.md)
