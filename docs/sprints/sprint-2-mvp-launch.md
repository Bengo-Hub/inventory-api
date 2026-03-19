# Sprint 2 - MVP Launch (March 17, 2026)

**Status:** ✅ P0 Events Done — BOM availability + reservation/consumption events + order auto-consume/release consumer implemented. Seed alignment, Atlas migration, and integration tests pending.
**Start:** 2026-03-06
**Deadline:** 2026-03-17
**Goal:** Ship inventory-api changes required for BengoBox MVP launch at Urban Loft Cafe (Busia outlet)

---

## Context

Sprint 1 delivered core schemas and 8 HTTP endpoints. The ordering-backend can now call inventory-api synchronously for stock checks and reservations. Sprint 2 closes the remaining gaps for a production-ready MVP.

---

## Tasks

### Recipe/BOM Mapping (P0)

- [ ] **S2-01:** Seed `recipe` and `recipeingredient` rows for all 39 menu items
  - Each menu-item SKU (e.g., `BEV-ESP-001`) gets a recipe record
  - Map composite items to their raw ingredients (e.g., Latte = espresso shot + steamed milk)
  - Simple items map 1:1 (finished good = inventory item)
- [x] **S2-02:** Add recipe-aware availability check ✅ DONE
  - When ordering-backend calls `GET /items/{sku}`, resolve recipe ingredients
  - Return availability based on the limiting ingredient (BOM explosion in `items/service.go`)
  - Fallback to direct item lookup if no recipe exists
- [ ] **S2-03:** Add recipe-aware reservation
  - `POST /reservations` should explode BOM and reserve raw ingredients
  - Response still shows the menu-item SKU but reserves underlying ingredients
  - Consume and release also operate on ingredient-level balances

### Seed Data Alignment (P0)

- [ ] **S2-04:** Cross-reference all 39 SKUs with ordering-backend `menu_items` table
  - Verify SKU strings match exactly (case-sensitive)
  - Verify categories align with ordering-backend's menu sections
  - Document any mismatches and fix in both services
- [ ] **S2-05:** Update warehouse seed: rename "Nairobi CBD" address to Busia address
  - The only active outlet is Busia, not Nairobi
  - Keep warehouse code `MAIN`

### Atlas Migration Transition (P0)

- [ ] **S2-06:** Install Atlas CLI and generate baseline migration from current Ent schemas
  - `atlas migrate diff --env ent` to generate initial migration files
  - Store in `migrations/` directory at repo root
- [ ] **S2-07:** Update `app.go` to run Atlas migrations instead of `ent.Schema.Create`
  - Remove auto-migrate call from `app.New()`
  - Add Atlas migration runner or use CLI in deployment pipeline
- [ ] **S2-08:** Test migration on a fresh database and on existing production schema
  - Ensure idempotency (re-running migration does not fail)

### Event Publishing (P0)

- [ ] **S2-09:** Emit `inventory.stock.updated` after balance changes
  - Add outbox row in the same transaction as balance update
  - Payload: `{ item_id, warehouse_id, on_hand, available, reserved }`
- [x] **S2-10:** Emit `inventory.reservation.confirmed` after successful reservation ✅ DONE
  - Also emits `inventory.reservation.released` and `inventory.stock.consumed` via outbox
  - `ordering.order.completed` → auto-consume reservation (`consumers/order_events.go`)
  - `ordering.order.cancelled` → auto-release reservation

### Integration Testing (P0)

- [ ] **S2-11:** Write integration test for the full reservation lifecycle
  - Create reservation -> verify balances -> consume -> verify balances
  - Create reservation -> release -> verify balances restored
  - Idempotency: duplicate reservation with same key returns original
- [ ] **S2-12:** Test ordering-backend -> inventory-api round-trip in staging
  - Place order in ordering-backend, verify reservation created
  - Complete order, verify stock consumed

### RBAC & Admin Separation (P1)

- [ ] **S2-13:** Define platform-admin vs tenant-admin role distinction
  - Platform admin: can manage all tenants, create warehouses, seed data
  - Tenant admin: can manage stock, view reports for their tenant only
- [ ] **S2-14:** Seed RBAC roles and permissions for MVP
  - `platform_admin`, `tenant_admin`, `stock_keeper`, `viewer`
  - Wire permission middleware to inventory endpoints

### Stock Adjustments (P1)

- [ ] **S2-15:** Add `POST /v1/{tenantID}/inventory/adjustments` endpoint
  - Allows manual stock corrections (waste, damage, recount)
  - Requires `inventory.stock.adjust` permission
  - Records reason code for audit trail

### Low-Stock Alerts (P1)

- [ ] **S2-16:** Add configurable low-stock threshold per item
  - When available drops below threshold after a mutation, emit `inventory.stock.low`
  - Notification service consumes event and sends alerts

---

## Definition of Done

- [ ] All P0 tasks complete and tested on staging
- [ ] ordering-backend successfully places orders with inventory stock checks
- [ ] Reservation -> consumption flow works end-to-end for all 39 menu items
- [ ] Atlas migrations generated and validated
- [ ] Event publishing verified via NATS monitoring
- [ ] No regressions in existing endpoints (integration tests pass)

---

## Dependencies

| Blocked By | Task |
|------------|------|
| ordering-backend menu item list | S2-04 (SKU cross-reference) |
| Atlas CLI available in CI | S2-06, S2-07 |
| NATS JetStream configured in staging | S2-09, S2-10 |
| notifications-service event consumer | S2-16 (low-stock alerts) |

---

## Out of Scope

- Multi-warehouse routing (post-MVP)
- Inventory UI (separate repo/sprint)
- Supplier/PO management
- Cycle counts and physical audits
- Demand forecasting
