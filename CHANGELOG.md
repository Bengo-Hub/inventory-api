# Changelog

All notable changes to the Inventory Service will be documented in this file.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added (2026-10-21 - Service / repair destination for purchase returns)
- **Repair orders:** a purchase return can now have `destination=service`, for in-stock items sent to a technician, service centre or care centre and expected back. The provider is free text in `purchase_returns.metadata`. Approving takes the items out as `service_out`; `POST /inventory/purchase-returns/{id}/receive` puts repaired units back as `service_in` with a restored cost layer and writes off unrepairable units as `damaged`. Statuses: `out`, `partial`, `returned`, `written_off`. No supplier credit note and no eTIMS movement is raised for a repair. Atlas migration `20261021132441_purchase_return_service_destination.sql`.
- `GET /inventory/purchase-returns/service-summary` (units and cost away, overdue, top providers); list filters `destination`, `service_status`, `overdue`. Stock history shows "Sent for Repair" and "Returned from Repair". Printable Service / Repair Order document.
- Events `inventory.service_order.created|sent|returned`. treasury-api skips the `service_out` and `service_in` reasons in its eTIMS stock handler.

### Fixed (2026-10-21 - purchase return audit)
- Approving a purchase return is all or nothing: a failed line reverses the lines already taken out and undoes the approval (409 `STOCK_OUT_FAILED`) instead of leaving a half approved return.
- `AdjustStock` no longer leaks a database transaction when asked to adjust a SERVICE item.
- Returns list: outlet and warehouse filter is a subselect (was an unbounded id list) and count errors are reported; PDF reads lots in one query; supplier lookups are tenant scoped; analytics top-items and stock-trends aggregate in SQL and leave repair movements out.

### Fixed (2026-09-05 — Add Product duplicate-SKU crash)
- **`GenerateSKU` was count-based, not max-based:** the next auto-generated SKU sequence number was derived from `COUNT(items with this prefix)`, which silently collides with an existing item's SKU the moment any item in the sequence has ever been hard-deleted (count drops below the true max in use). Found live on boi-enterprises' Add Product, surfacing a raw `ent: constraint failed: ERROR: duplicate key value violates unique constraint "item_tenant_id_sku"` straight to the UI. Now derived from the **max existing numeric suffix** instead.
- **`CreateItem` now retries on a SKU collision** (bounded, 5 attempts) when the SKU was auto-generated, closing the remaining true-concurrency window (two requests landing on the same next number at once) instead of failing outright.
- **New `items.DuplicateSKUError`**, mapped by the `CreateItem` handler to a clean `409 DUPLICATE_SKU` — mirrors the existing `units.DuplicateUnitError` pattern. An explicitly-typed (not auto-generated) SKU that collides with an existing item now gets this clean error instead of the raw DB constraint text.
- **inventory-ui**: the Add/Edit Product form now does a live, debounced exact-SKU lookup (`GET /inventory/items/{sku}`) as the SKU field is typed and blocks Save with an inline message when it's already taken — catches the conflict before submission instead of only after a 409.

### Added (2026-07-20 — End-of-Life products)
- **EOL lifecycle:** New nullable `Item.end_of_life_at` field + index `item_tenant_id_end_of_life_at` (Atlas migration `20260720120000_add_item_end_of_life.sql`). Non-null = the item is marked End-of-Life.
- **Mark / restore endpoints:** `POST /inventory/items/{sku}/eol` and `POST /inventory/items/{sku}/eol/restore` (both require `inventory.items.delete`). Marking sets `is_active=false` + `end_of_life_at=now` in one transaction and emits an enriched `inventory.item.updated` outbox event, so the item disappears immediately from item lists, the POS live catalog (fetched with `status=active`), and ordering, and the pos-api catalog consumer flips `pos_catalog_override.is_available=false` + bumps the catalog version. Restore clears the timestamp and re-activates.
- **EOL listing:** `GET /inventory/items?status=eol` returns only EOL items (the dedicated "End of Life" tab). `end_of_life_at` added to `ItemDTO` and to item event payloads. Plain `status=inactive` now excludes EOL items so the EOL tab owns them.
- **Purge scheduler:** New advisory-lock-guarded, tenant-generic `EOLPurgeScheduler` (`EOL_PURGE_ENABLED`, `EOL_RETENTION_DAYS` default 7) hard-deletes items whose retention window has elapsed. Audit-safe: items with transactional history (PO/GRN/adjustment/stock-level-event/transfer/return/requisition/RFQ/stock-count/serial/daily-consumption lines, or used as a recipe ingredient / bundle component elsewhere) are skipped and kept hidden; only owned catalog children (balances, pricing, translations, assets, custom-field values, warranties, lots, own modifier groups/options, variants, bundle, produced recipe) are removed alongside the item.

### Added (2026-05-27 — Batch 2: UX, RBAC & ERP polish)
- **Unit type field:** Added `type` (weight/volume/count/length/area/other) to `Unit` schema and Atlas migration `20260527144311_add_unit_type.sql`. `GET /units` now returns `type` and `item_count` per unit.
- **Items filter params:** `GET /inventory/items` now accepts `category_id`, `unit_id`, and `search` query params for server-side filtering.
- **Category hierarchy:** `GET /inventory/categories` now returns `parent_id` and `parent_name`. `POST`/`PUT` category endpoints accept and persist `parent_id` for subcategory creation.
- **Demo supplier seed:** `cmd/seed` seeds "Demo Distributor Co." supplier for `codevertex-demo` tenant and enables auto-reorder on 3 items (BEV-ESP-001, BEV-CAP-001, BEV-LAT-001) via `seedSuppliers` + `seedReorderConfig`.

### Fixed (2026-05-27 — Batch 2)
- **RBAC 403 for admin users:** `RequirePermission` and `RequireAnyPermission` middleware now bypass permission checks for `admin`, `manager`, `super_admin`, and `store_manager` roles in addition to `inventory_admin`. Demo tenant admins with the `admin` role can now access all guarded endpoints.
- **ListItems cache invalidation:** `search`, `category_id`, and `unit_id` filters correctly bypass the Redis cache for dynamic queries.

### Changed (2026-05-27 — Batch 2)
- `UnitDTO` extended with `type` (string) and `item_count` (int) fields.
- `CategoryDTO` extended with `parent_name` (string) and now always populates `parent_id`.
- `ListItems` service function signature extended: `categoryID *uuid.UUID`, `unitID *uuid.UUID`, `search string` added before the variadic `tagsFilter`.

---

### Changed
- Standardized API base path to `/api/v1` (previously `/v1`)
- Standardized Swagger documentation path to `/v1/docs` (previously `/swagger/*`)
- Updated OpenAPI specification servers to use HTTPS URLs for local development
- Updated Swagger specifications to support both HTTP and HTTPS schemes
- Replaced `http-swagger` with custom Swagger handler that embeds OpenAPI spec and provides protocol-aware URL detection for HTTPS compatibility
- Swagger UI now displays standard header with Explore button and URL input field

### Added
- Added service delivery plan (`plan.md`) covering scope and roadmap.
- Authored ERD (`docs/erd.md`) outlining core entities and integrations.
- Created repository scaffolding (README, contribution guide, security policy, support docs).
- **Service Bootstrap:** Complete Go service scaffolding with HTTP server, configuration, logging, health endpoints, and Swagger documentation.
- **Auth-Service SSO Integration:** Integrated `shared/auth-client` v0.1.0 library for production-ready JWT validation using JWKS from auth-service. All protected `/v1/{tenantID}` routes require valid Bearer tokens. Swagger documentation updated with BearerAuth security definition. Uses monorepo `replace` directives with versioned dependency. See `shared/auth-client/DEPLOYMENT.md` and `shared/auth-client/TAGGING.md` for details.
- **Infrastructure:** PostgreSQL connection pool, Redis caching, NATS event bus integration, Prometheus metrics, structured logging with zap.

### Changed
- Service now uses Go workspace (`go.work`) for local development; production deployments consume `shared/auth-client` as a private Go module.

### Pending
- Ent schema implementation
- CI/CD automation
- Domain-specific handlers and business logic

