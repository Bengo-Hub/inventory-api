# Sprint: ERP E-commerce Gaps — inventory-api

**Created:** April 2026
**Status:** In Progress (Gaps 3 & 4 complete, Gaps 1 & 2 pending)
**Goal:** Close feature gaps identified from ERP ecommerce/stock module audit before ERP module deletion (Phase 1)

---

## Context

The ERP `ecommerce/stockinventory/` and `ecommerce/product/` modules contain sub-warehouse location tracking, bulk pricing tiers, low-stock alert publishing, and stock consumption event wiring that may not yet be fully covered by inventory-api. These must be verified and implemented before the ERP modules can be removed.

---

## Gap 1: Sub-Warehouse Locations (Bin / Shelf / Aisle)

**ERP source:** `ecommerce/stockinventory/` — `WarehouseLocation` model (bin, shelf, aisle, zone)
**Priority:** P1
**Status:** Pending — verify WarehouseZone coverage

### Current State

inventory-api has `WarehouseZone` for logical partitioning of warehouses. The ERP module had a more granular `WarehouseLocation` with a hierarchy: Warehouse > Zone > Aisle > Shelf > Bin.

### Required

- [ ] **INV-ERP-01:** Audit `WarehouseZone` schema — confirm it supports:
  - Hierarchical location structure (zone > aisle > shelf > bin)
  - Location code (e.g. `A-03-02` = Aisle A, Shelf 3, Bin 2)
  - Location type (zone/aisle/shelf/bin)
  - Parent-child relationship (self-referential FK)
  - Capacity tracking (optional)
- [ ] **INV-ERP-02:** If `WarehouseZone` is flat (no hierarchy), extend or add `WarehouseLocation` Ent schema:
  - Fields: `id`, `warehouse_id` (FK), `parent_id` (self-FK, nullable), `name`, `code`, `location_type` (zone/aisle/shelf/bin), `depth`, `path`, `capacity`, `is_active`
  - This mirrors the hierarchical category pattern already used for `ItemCategory`
- [ ] **INV-ERP-03:** Generate Atlas migration if schema changes needed
- [ ] **INV-ERP-04:** Add location assignment to `InventoryBalance`
  - Optional `location_id` FK on balance records to track stock by location
  - Supports pick/put-away workflows
- [ ] **INV-ERP-05:** Add location handlers
  - `POST /api/v1/{tenant}/warehouses/{warehouse_id}/locations` — create location
  - `GET /api/v1/{tenant}/warehouses/{warehouse_id}/locations` — list locations (tree)
  - `PATCH /api/v1/{tenant}/locations/{id}` — update location
  - `GET /api/v1/{tenant}/locations/{id}/stock` — get stock at location

---

## Gap 2: Bulk / Quantity Pricing Tiers

**ERP source:** `ecommerce/product/` — `PriceTier` model (buy X+ units at price Y)
**Priority:** P2
**Status:** Pending

### Current State

inventory-api stores `cost_price` and `selling_price` on Items/Variants. There is no quantity-based pricing tier model (e.g. 1-9 units at $10, 10-49 at $8, 50+ at $6).

### Required

- [ ] **INV-ERP-06:** Add `PricingTier` Ent schema
  - Fields: `id`, `item_id` (FK), `variant_id` (FK, nullable), `min_quantity`, `max_quantity` (nullable = unlimited), `unit_price`, `currency`, `price_list_id` (FK, nullable — for multi-price-list support), `is_active`
  - Constraint: tiers for same item must not have overlapping quantity ranges
- [ ] **INV-ERP-07:** Generate Atlas migration
- [ ] **INV-ERP-08:** Add pricing tier handlers
  - `POST /api/v1/{tenant}/items/{item_id}/pricing-tiers` — create tier
  - `GET /api/v1/{tenant}/items/{item_id}/pricing-tiers` — list tiers
  - `PUT /api/v1/{tenant}/pricing-tiers/{id}` — update tier
  - `DELETE /api/v1/{tenant}/pricing-tiers/{id}` — delete tier
- [ ] **INV-ERP-09:** Add pricing resolution logic
  - `GET /api/v1/{tenant}/items/{item_id}/price?quantity=N` — resolve effective price for quantity
  - Used by ordering-backend and pos-api for cart pricing
- [ ] **INV-ERP-10:** Publish `inventory.item.pricing_updated` event when tiers change
  - Subscribers: pos-api (catalog sync), ordering-backend (catalog cache invalidation)

---

## Gap 3: Low-Stock Alert Event Publishing

**ERP source:** `ecommerce/stockinventory/` — `StockAlert`, `LowStockRule` models
**Priority:** P1
**Status:** Pending

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
**Status:** Pending

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
