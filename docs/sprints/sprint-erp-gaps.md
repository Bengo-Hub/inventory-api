# Sprint: ERP E-commerce Gaps — inventory-api

**Created:** April 2026
**Status:** ✅ Complete — All four gaps closed; `location_id` FK on `InventoryBalance` added (INV-ERP-04 ✅ 2026-05-27); pricing resolution endpoint `GET /items/{id}/price?quantity=N` shipped (INV-ERP-09 ✅ 2026-05-27); `inventory.item.pricing_updated` outbox event published on `UpsertItemPricing` (INV-ERP-10 ✅ 2026-05-27); analytics dashboard endpoints added (top-items, stock-trends, distribution, reorder-alerts, enhanced-summary ✅ 2026-05-27); integration tests and notifications-api subscriber still pending
**Goal:** Close feature gaps identified from ERP ecommerce/stock module audit before ERP module deletion (Phase 1)

---

## Context

The ERP `ecommerce/stockinventory/` and `ecommerce/product/` modules contain sub-warehouse location tracking, bulk pricing tiers, low-stock alert publishing, and stock consumption event wiring that may not yet be fully covered by inventory-api. These must be verified and implemented before the ERP modules can be removed.

---

## Gap 1: Sub-Warehouse Locations (Bin / Shelf / Aisle)

**ERP source:** `ecommerce/stockinventory/` — `WarehouseLocation` model (bin, shelf, aisle, zone)
**Priority:** P1
**Status:** ✅ Complete — schemas + handlers shipped; `location_id` FK on `InventoryBalance` added (2026-05-27)

### Current State

inventory-api has `WarehouseZone` for logical partitioning of warehouses. The ERP module had a more granular `WarehouseLocation` with a hierarchy: Warehouse > Zone > Aisle > Shelf > Bin.

### Required

- [x] **INV-ERP-01:** `WarehouseLocation` Ent schema confirmed: `internal/ent/schema/warehouselocation.go`
- [x] **INV-ERP-02:** `WarehouseLocation` schema implemented (not flat WarehouseZone; separate dedicated schema)
- [x] **INV-ERP-03:** Atlas migration generated for `WarehouseLocation`
- [x] **INV-ERP-04:** `location_id` FK on `InventoryBalance` — optional UUID field + edge to `WarehouseLocation` present in `internal/ent/schema/inventorybalance.go` (field line 53, edge line 76). Atlas migration generated. (2026-05-27)
- [x] **INV-ERP-05:** Location handlers implemented (`warehouse_location.go`) and registered in router via `warehouseLocationHandler.RegisterRoutes(g)`

---

## Gap 2: Bulk / Quantity Pricing Tiers

**ERP source:** `ecommerce/product/` — `PriceTier` model (buy X+ units at price Y)
**Priority:** P2
**Status:** ✅ Complete — CRUD handlers, bulk pricing list, quantity-aware price resolution, and `inventory.item.pricing_updated` outbox event all shipped (2026-05-27)

### Current State

inventory-api stores `cost_price` and `selling_price` on Items/Variants. There is no quantity-based pricing tier model (e.g. 1-9 units at $10, 10-49 at $8, 50+ at $6).

### Required

- [x] **INV-ERP-06:** `PricingTier` Ent schema at `internal/ent/schema/pricingtier.go`; `ItemPricing` schema at `internal/ent/schema/itempricing.go`
- [x] **INV-ERP-07:** Atlas migration generated for pricing tier schemas
- [x] **INV-ERP-08:** Pricing tier handlers implemented (`pricing_tier.go`) and registered via `pricingTierHandler.RegisterRoutes(g)`
- [x] **INV-ERP-09:** Pricing resolution endpoint `GET /inventory/items/{itemID}/price?quantity=N` — implemented in `pricing_tier.go` (`GetItemPrice` handler, line 489). Selects default tier; falls back to first active tier; returns `unit_price`, `total_price`, `tier_id/name/code`, `quantity`. (2026-05-27)
- [x] **INV-ERP-10:** `inventory.item.pricing_updated` outbox event — `publishPricingUpdatedEvent()` called as goroutine in `UpsertItemPricing` after each successful save. Writes outbox row with subject `inventory.item.pricing_updated`, payload includes `item_id`, `pricing_tier_id`, `price`, `currency`, `effective_from`. (2026-05-27)

---

## Gap 3: Low-Stock Alert Event Publishing

**ERP source:** `ecommerce/stockinventory/` — `StockAlert`, `LowStockRule` models
**Priority:** P1
**Status:** ✅ Done — `stock.low` and `stock.out` events published via outbox after every balance decrease; notifications-api subscriber still pending

### Current State

inventory-api tracks `reorder_point` and `reorder_quantity` on `InventoryBalance` and has `auto_reorder_enabled`. The `inventory.stock.low` event subject is listed in the cross-service event matrix but publishing may not be fully implemented.

### Required

- [x] **INV-ERP-11:** `inventory.stock.low` is published via `checkAndPublishLowStock()` in stock service
  - Emitted within the balance-update transaction (outbox pattern) after AdjustStock and RecordConsumption
  - Payload includes: `item_id`, `sku`, `name`, `available`, `reorder_level`, `warehouse_id`, `tenant_id`
- [x] **INV-ERP-12:** Implemented in `internal/modules/stock/service.go:checkAndPublishLowStock()`
  - Called after every balance decrease (adjustment, reservation consume, direct consumption)
- [x] **INV-ERP-13:** `inventory.stock.out` event published when `available <= 0` (same function)
- [ ] **INV-ERP-14:** Verify notifications-api subscribes to `inventory.stock.low` and sends alerts (pending)

---

## Gap 4: Stock Consumption Event Consumer Wiring

**ERP source:** `ecommerce/stockinventory/` — consumption triggered by order fulfillment and POS sale
**Priority:** P0
**Status:** ✅ Done — all four consumers implemented and registered; integration tests still pending

### Current State

inventory-api has a `POST /consumption` endpoint and consumes `pos.sale.finalized` for POS backflush. The `ordering.order.completed` consumer may not be fully wired.

### Required

- [x] **INV-ERP-15:** `ordering.order.completed` consumer exists: `internal/modules/consumers/order_events.go`
  - Auto-consumes/releases reservation; idempotent (skips if reservation already consumed)
- [x] **INV-ERP-16:** `pos.sale.finalized` consumer exists: `internal/modules/consumers/pos_sale_events.go`
  - Full BOM explosion for RECIPE items; idempotent via `IdempotencyKey`
- [ ] **INV-ERP-17:** Integration tests for consumers — pending
- [x] **INV-ERP-18:** `ordering.return.approved` consumer: `internal/modules/consumers/return_events.go`
  - Restocks returned items via `stock.RestockItems()`; idempotent key: `ordering-return-{return_id}`
- [x] **INV-ERP-19:** `pos.return.completed` consumer: `internal/modules/consumers/return_events.go`
  - Restocks returned items via `stock.RestockItems()`; idempotent key: `pos-return-{return_id}`

---

## References

- [ERP Module Removal Plan](../../../../erp/erp-api/docs/module-removal-plan.md)
- [Cross-Service Data Ownership](../../../../shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md)
- [Inventory Integrations](../integrations.md)
- [Inventory Architecture](../architecture.md)
