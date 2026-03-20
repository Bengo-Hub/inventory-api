# Inventory API - MVP Plan

**Last updated:** 2026-03-20

**March 20 update:** Added item CRUD endpoints (`GET /items`, `POST /items`, `PUT /items/{sku}`), category listing (`GET /categories`), and `ListItems`/`ListCategories` service methods. Warehouse seed now uses deterministic UUID matching ordering-backend's outlet UUID formula for cross-service ID alignment. These endpoints enable ordering-backend and pos-api to create/verify inventory items via REST when users add menu items from the UI.
**MVP deadline:** 2026-03-27
**Tenant:** urban-loft (Urban Loft Cafe)
**Active outlet:** Busia (main)
**Production domain:** `inventoryapi.codevertexitsolutions.com`

---

## Current State (Sprint 1 - Complete)

Sprint 1 delivered the full inventory business layer from scratch:

- **7 Ent schemas:** item, warehouse, inventorybalance, reservation, consumption, recipe, recipeingredient
- **8 HTTP endpoints:** stock availability (single + bulk), reservation CRUD (create, get, get-by-order, release, consume), direct consumption
- **39 seed items** across 7 categories for Urban Loft Cafe (Busia warehouse `MAIN`)
- **Shared library alignment:** httpware v0.2.0, shared-events v0.2.0, shared-auth-client v0.3.1
- **Infrastructure:** PostgreSQL (Ent auto-migrate), Redis, NATS JetStream, outbox publisher, JWT + API key auth
- **User management:** RBAC service and user-sync stubs wired into app bootstrap

The ordering-backend's inventory client (`internal/platform/inventory/client.go`) now calls these endpoints synchronously for stock checks and reservations during order placement. **SKU alignment (Mar 6)**: Ordering-backend seed creates 39 menu items with the same SKUs as this service; Recipe/BOM schemas exist; warehouse seed uses "Urban Loft Busia Kitchen" (Busia). **inventory-ui**: Next.js app scaffolded at `inventory-service/inventory-ui/` (SSO, catalog, warehouses, adjustments, platform admin); needs API wiring and deploy.

---

## MVP Scope (March 27 Deliverables)

### P0 - Must Ship

| # | Task | Owner | Status |
|---|------|-------|--------|
| 1 | Recipe/BOM mapping: seed recipes linking menu-item SKUs to raw ingredient items | Backend | **Done** |
| 2 | BOM-aware stock check: availability endpoint resolves recipe ingredients, not just finished-good SKU | Backend | Not started |
| 3 | Seed data alignment: ensure all 39 SKUs match ordering-backend menu items exactly (Busia outlet) | Backend | **Done** |
| 4 | Reservation-to-consumption flow: ordering-backend emits `order.completed` -> inventory consumes reservation | Backend | Not started |
| 5 | Atlas migration transition | Backend | **Done** (initial schema and shared_units generated) |
| 6 | Integration test: full reservation -> consume -> balance check round-trip | Backend | Not started |
| 7 | NATS event publishing: emit `inventory.stock.updated` and `inventory.reservation.confirmed` on mutations | Backend | **Done** ✅ DONE |

### P1 - Should Ship

| # | Task | Status |
|---|------|--------|
| 8 | Platform admin vs tenant admin role separation in RBAC seed | Not started |
| 9 | Stock adjustment endpoint (admin manual corrections) | Not started |
| 10 | Low-stock threshold alerts via notifications-service event | Not started |
| 11 | Health-check alignment with httpware v0.2.0 standard probes | Not started |

### P2 - Nice to Have

| # | Task | Status |
|---|------|--------|
| 12 | Superset read-only DB user provisioning for BI dashboards | Not started |
| 13 | Bulk item import endpoint (CSV/JSON) | Not started |

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
