# Inventory API - MVP Plan

**Last updated:** 2026-05-22

**March 20 update:** Added item CRUD endpoints (`GET /items`, `POST /items`, `PUT /items/{sku}`), category listing (`GET /categories`), and `ListItems`/`ListCategories` service methods. Warehouse seed now uses deterministic UUID matching ordering-backend's outlet UUID formula for cross-service ID alignment. These endpoints enable ordering-backend and pos-api to create/verify inventory items via REST when users add menu items from the UI.
**MVP deadline:** 2026-03-27
**Tenant:** urban-loft (Urban Loft Cafe)
**Active outlet:** Busia (main)
**Production domain:** `inventoryapi.codevertexitsolutions.com`

---

## Current State (2026-05-21)

Sprint 1 + Sprint 2 + ERP Gaps sprint are substantially complete. The following is the actual shipped state:

**Ent Schemas (38 files):** item, warehouse, inventorybalance, inventorylot, reservation (consumption), recipe, recipeingredient, unit, supplier, purchaseorder, purchaseorderline, stockadjustment, stocktransfer, stocktransferline, itemcategory, itemvariant, itemasset, itemtranslation, variantattribute, modifiergroup, modifieroption, bundle, bundlecomponent, customfielddefinition, customfieldvalue, warranty, outboxevent, rate_limit_config, service_config, tenant, inventory_role, inventory_permission, inventory_user, user_role_assignment, role_permission, warehouselocation, pricingtier, itempricing

**HTTP Handlers:** inventory.go, warehouse.go, warehouse_location.go, pricing_tier.go, transfers.go, user.go, rbac.go, media.go, modifiers.go

**RBAC:** 4 roles (`inventory_admin`, `warehouse_manager`, `stock_clerk`, `viewer`), 99 permissions. JIT user provisioning wired. Role assignment API: POST/GET/DELETE `/rbac/assignments`.

**Event Consumers:** `ordering.order.completed` → auto-consume reservation; `ordering.order.cancelled` → auto-release; `pos.sale.finalized` → BOM backflush; `ordering.return.approved` + `pos.return.completed` → restock.

**Event Publishers:** `inventory.stock.updated`, `inventory.reservation.confirmed`, `inventory.reservation.released`, `inventory.stock.consumed`, `inventory.stock.low`, `inventory.stock.out` (all via outbox pattern).

**inventory-ui:** ✅ Complete — SSO, multi-outlet, all pages data-integrated including post-MVP Phase 15 pages (recipe viewer, POs, suppliers, transfers, lots, reservations, warehouse locations, pricing tiers). Full CRUD added: Items (create/edit/delete from catalog), Warehouses (create/edit/delete + locations tree), PO creation dialog with ItemSearchInput, delete actions on categories/units/suppliers, ItemSearchInput autocomplete in adjustments and transfers. URL prefix bugs fixed in modifiers and settings. Standalone modifier-group list/get endpoints added.

---

## MVP Scope Status (2026-05-21)

### P0 - Must Ship

| # | Task | Status |
|---|------|--------|
| 1 | Recipe/BOM mapping: seed recipes linking menu-item SKUs to raw ingredient items | ⚠️ Schema seeded; menu-item recipe rows pending (S2-01) |
| 2 | BOM-aware stock check: availability endpoint resolves recipe ingredients | ✅ Done |
| 3 | Seed data alignment: all 39 SKUs match ordering-backend | ✅ Done |
| 4 | Reservation-to-consumption flow (`order.completed` consumer) | ✅ Done |
| 5 | Atlas migration transition | ✅ Done |
| 6 | Integration test: full reservation → consume lifecycle | ❌ Not started |
| 7 | NATS event publishing | ✅ Done |

### P1 - Should Ship

| # | Task | Status |
|---|------|--------|
| 8 | Platform admin vs tenant admin RBAC seed | ⚠️ 4 roles seeded; `platform_admin` distinction pending |
| 9 | Stock adjustment endpoint | ✅ Done (via inventory handler) |
| 10 | Low-stock alerts | ✅ Done (`inventory.stock.low` published via `checkAndPublishLowStock`) |
| 11 | Health-check alignment | ✅ Done |

### P2 - Nice to Have

| # | Task | Status |
|---|------|--------|
| 12 | Superset read-only DB user | ❌ Not started |
| 13 | Bulk item import endpoint | ✅ Done (`POST /inventory/items/import`, CSV upsert) |

## Remaining Gaps

1. **RBAC seed data**: default roles/permissions not yet seeded via migration or seed script (users JIT-provisioned but no role-permission rows)
2. **Recipe seed rows**: 39 menu-item → ingredient recipe mappings not seeded
3. **Integration tests**: reservation lifecycle tests not written
4. **Notifications-api subscriber**: `inventory.stock.low` event not confirmed consumed by notifications-api
5. **Pricing resolution endpoint**: `GET /items/{id}/price?quantity=N` not confirmed registered
6. **InventoryBalance.location_id**: FK not confirmed in schema

---

## Architecture Constraints for MVP

- **Single warehouse only** (Busia `MAIN`). Multi-warehouse routing is post-MVP.
- **Synchronous stock checks** from ordering-backend. Async event-driven decoupling is post-MVP.
- **Ent auto-migrate on startup** until Atlas migration files are generated and validated.
- **No UI** ships for inventory-api MVP. The inventory-ui is a separate deliverable with its own timeline.

---

## Dependencies

| Dependency | Version | Notes |
|------------|---------|-------|
| entgo.io/ent | v0.14.5 | ORM + code generation |
| shared-auth-client | v0.3.1 | JWT/JWKS + API key auth |
| httpware | v0.2.0 | HTTP middleware, health probes |
| shared-events | v0.2.0 | NATS JetStream helpers, outbox |
| pgx/v5 | latest | PostgreSQL driver |

---

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| SKU mismatch between ordering menu and inventory seed | Orders fail stock check | Cross-reference seed data with ordering-backend `MenuItems` before March 10 |
| Recipe/BOM not seeded in time | Ingredient-level tracking unavailable | Fallback: treat finished-good SKU as 1:1 with inventory item (no BOM explosion) |
| Atlas migration breaks existing data | Production downtime | Generate Atlas baseline from current schema, test on staging before cutover |
