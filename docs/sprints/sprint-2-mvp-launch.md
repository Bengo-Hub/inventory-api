# Sprint 2 - MVP Launch (March 27, 2026)

**Status:** 🟡 P0 Core Done — BOM availability + reservation/consumption events + order auto-consume/release consumer implemented; recipe seed (39 items) and RBAC seed fully done in `cmd/seed/main.go`. SKU cross-reference with ordering-backend, integration tests, and RBAC role distinction (S2-13/S2-14) still pending.
**Start:** 2026-03-06
**Deadline:** 2026-03-27
**Goal:** Ship inventory-api changes required for BengoBox MVP launch at Urban Loft Cafe (Busia outlet)

---

## Context

Sprint 1 delivered core schemas and 8 HTTP endpoints. The ordering-backend can now call inventory-api synchronously for stock checks and reservations. Sprint 2 closes the remaining gaps for a production-ready MVP.

---

## Tasks

### Recipe/BOM Mapping (P0)

- [x] **S2-01:** Seed `recipe` and `recipeingredient` rows for all 39 menu items ✅ DONE (2026-05-26 audit)
  - `cmd/seed/main.go:seedRecipes()` seeds all RECIPE-type SKUs in `recipeDefs` (39 menu items) for `urban-loft` tenant only
  - Full BOM mapping: composite items to raw ingredients (e.g., Latte = espresso shot + steamed milk)
  - Raw ingredient items seeded in `catalogItemDefs` as `TypeINGREDIENT`
- [x] **S2-02:** Add recipe-aware availability check ✅ DONE
  - When ordering-backend calls `GET /items/{sku}`, resolve recipe ingredients
  - Return availability based on the limiting ingredient (BOM explosion in `items/service.go`)
  - Fallback to direct item lookup if no recipe exists
- [x] **S2-03:** Recipe-aware reservation ✅ DONE
  - `POST /reservations` explodes BOM via `explodeBOM()` and reserves raw ingredients
  - Response shows menu-item SKU summary; ingredient-level reservations in `items` field
  - Consume and release operate on ingredient-level balances

### Seed Data Alignment (P0)

- [x] **S2-04 (bulk import):** `POST /inventory/items/import` — CSV bulk upsert endpoint added ✅ 2026-05-22
- [ ] **S2-04-legacy:** Cross-reference all 39 SKUs with ordering-backend `menu_items` table
  - Verify SKU strings match exactly (case-sensitive)
  - Verify categories align with ordering-backend's menu sections
  - Document any mismatches and fix in both services
- [x] **S2-05:** Warehouse seed uses correct Busia address ✅ DONE (2026-05-26 audit)
  - `cmd/seed/main.go:warehouseDefsByTenant["urban-loft"]` seeds `"Main Street, Busia Town, Busia County, Kenya"` with code `MAIN`
  - Outlet slug `"busia"` aligned with auth-api deterministic UUID formula

### Atlas Migration Transition (P0)

- [x] **S2-06:** Install Atlas CLI and generate baseline migration from current Ent schemas
  - `atlas migrate diff --env ent` to generate initial migration files
  - Store in `migrations/` directory at repo root
- [x] **S2-07:** Update `app.go` to run Atlas migrations instead of `ent.Schema.Create`
  - Remove auto-migrate call from `app.New()`
  - Add Atlas migration runner or use CLI in deployment pipeline
- [x] **S2-08:** Test migration on a fresh database and on existing production schema
  - Ensure idempotency (re-running migration does not fail)

### Event Publishing (P0)

- [x] **S2-09:** Emit `inventory.stock.updated` after balance changes ✅ DONE
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

- [ ] **S2-13:** Define platform-admin vs tenant-admin role distinction — **PENDING**
  - Current roles are `inventory_admin`, `warehouse_manager`, `stock_clerk`, `viewer` (all tenant-scoped)
  - No explicit platform-admin vs tenant-admin separation in current seed; platform owner uses JWT `is_platform_owner` bypass
  - Platform admin: can manage all tenants, create warehouses, seed data — not yet modelled as an explicit role
- [x] **S2-14:** Seed RBAC roles and permissions — **DONE** (2026-05-26 audit, done differently from original spec)
  - Roles seeded: `inventory_admin` (all 99 perms), `warehouse_manager`, `stock_clerk`, `viewer`
  - Note: original spec listed `platform_admin` / `tenant_admin` roles; actual implementation uses `inventory_admin` as the top tier instead
  - Permission middleware wired to inventory endpoints via `perm()` helper in all handler `RegisterRoutes()`

### Stock Adjustments (P1)

- [x] **S2-15:** Add `POST /v1/{tenantID}/inventory/adjustments` endpoint
  - Allows manual stock corrections (waste, damage, recount)
  - Requires `inventory.stock.adjust` permission
  - Records reason code for audit trail

### Low-Stock Alerts (P1)

- [x] **S2-16:** Add configurable low-stock threshold per item
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
