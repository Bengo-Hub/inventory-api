# Inventory API - Architecture

**Service:** inventory-api
**Language:** Go 1.22+
**ORM:** Ent (entgo.io/ent v0.14.5)
**HTTP Router:** chi/v5
**Port:** 4003
**Production:** `inventoryapi.codevertexafrica.com`
**Last updated:** 2026-05-21
**Status:** Sprint 1 + Sprint 2 + ERP Gaps substantially complete. 38 Ent schemas, 12 handler files, 4 RBAC roles, 99 permissions, full outbox event publishing, NATS event consumers wired.

---

## High-Level Overview

```
                         ┌────────────────────┐
                         │  ordering-backend   │
                         │  (synchronous HTTP) │
                         └────────┬───────────┘
                                  │
                    REST: stock checks, reservations
                                  │
                         ┌────────▼───────────┐
                         │   inventory-api     │
                         │   :4003             │
                         ├────────────────────┤
                         │ chi router          │
                         │ JWT + API key auth  │
                         │ InventoryHandler    │
                         ├────────────────────┤
                         │ items.Service       │
                         │ stock.Service       │
                         ├────────────────────┤
                         │ Ent ORM (Postgres)  │
                         │ Redis (cache)       │
                         │ NATS (events)       │
                         └────────────────────┘
```

---

## Project Layout

```
inventory-api/
├── cmd/
│   ├── server/main.go          # Application entrypoint
│   └── seed/main.go            # Seed data CLI
├── internal/
│   ├── app/app.go              # Bootstrap: DB, Redis, NATS, auth, modules, HTTP server
│   ├── config/config.go        # Environment-based config (INVENTORY_ prefix)
│   ├── ent/                    # Ent generated code
│   │   └── schema/             # Ent schema definitions (source of truth)
│   │       ├── item.go
│   │       ├── warehouse.go
│   │       ├── inventorybalance.go
│   │       ├── reservation.go
│   │       ├── consumption.go
│   │       ├── recipe.go
│   │       └── recipeingredient.go
│   ├── http/
│   │   ├── handlers/
│   │   │   └── inventory.go    # 8 HTTP endpoint handlers
│   │   └── router/router.go    # chi route registration
│   ├── modules/
│   │   ├── items/service.go    # GetStockAvailability, BulkAvailability
│   │   ├── stock/service.go    # Reservation CRUD, consumption
│   │   └── outbox/             # Transactional outbox publisher
│   ├── platform/
│   │   ├── cache/              # Redis client init
│   │   ├── database/           # pgxpool init
│   │   └── events/             # NATS connection + outbox adapter
│   ├── services/
│   │   ├── rbac/               # Role-based access control
│   │   └── usersync/           # User sync with auth-service
│   └── shared/
│       └── logger/             # Zap logger init
├── docs/                       # Documentation
└── go.mod
```

---

## Ent Schemas (38 entities — as of 2026-05-21)

| Schema | Purpose |
|--------|---------|
| `item` | Canonical SKU catalogue |
| `warehouse` | Physical storage locations |
| `inventorybalance` | Stock levels per item per warehouse |
| `inventorylot` | Lot/batch tracking with expiry dates |
| `reservation` | Order-level stock reservations |
| `recipe` | BOM header: maps menu-item SKU to a recipe |
| `recipeingredient` | BOM line: links recipe to raw inventory items |
| `unit` | Units of measure (UoM) |
| `supplier` | Supplier master records |
| `purchaseorder` | Purchase order headers |
| `purchaseorderline` | Purchase order line items |
| `stockadjustment` | Inventory adjustment records |
| `stocktransfer` | Inter-warehouse stock transfer headers |
| `stocktransferline` | Stock transfer line items |
| `itemcategory` | Item category hierarchy |
| `itemvariant` | Item variant definitions (size, color, etc.) |
| `itemasset` | Item media assets |
| `itemtranslation` | Multi-language item names/descriptions |
| `variantattribute` | Variant attribute key-value pairs |
| `modifiergroup` | Item modifier groups |
| `modifieroption` | Modifier group options |
| `bundle` | Bundle/kit product headers |
| `bundlecomponent` | Bundle component lines |
| `customfielddefinition` | Custom field definitions per tenant |
| `customfieldvalue` | Custom field values per item |
| `warranty` | Item warranty records |
| `outboxevent` | Transactional outbox for NATS publishing |
| `rate_limit_config` | Per-tenant/IP rate limiting configuration |
| `service_config` | Service-level configuration settings |
| `tenant` | Tenant registry |
| `inventory_role` | RBAC role definitions |
| `inventory_permission` | RBAC permission definitions |
| `inventory_user` | JIT-provisioned user records |
| `user_role_assignment` | User to role mapping |
| `role_permission` | Role to permission mapping |
| `warehouselocation` | Warehouse bin/zone/aisle location tree |
| `pricingtier` | Pricing tier definitions |
| `itempricing` | Item price per pricing tier |

---

## HTTP Handler Files (as of 2026-05-21)

| File | Module |
|------|--------|
| `handlers/inventory.go` | Items, availability, reservations, consumption, adjustments |
| `handlers/warehouse.go` | Warehouse CRUD |
| `handlers/warehouse_location.go` | Warehouse location tree |
| `handlers/pricing_tier.go` | Pricing tiers |
| `handlers/transfers.go` | Stock transfers |
| `handlers/user.go` | User management (JIT provisioning) |
| `handlers/rbac.go` | RBAC role/permission assignment |
| `handlers/media.go` | Item asset upload |
| `handlers/modifiers.go` | Modifier groups and options |
| `handlers/tenant.go` | Tenant provisioning |
| `handlers/health.go` | Health probe |
| `handlers/swagger.go` | OpenAPI docs |

## HTTP Endpoints

All routes are mounted under `/v1/{tenantID}/inventory/`.

**Core inventory (inventory.go):**

| Method | Path | Description |
|--------|------|-------------|
| GET | `/items` | List items (catalog sync) |
| GET | `/items/{sku}` | Single item stock check |
| POST | `/items` | Create item |
| PUT | `/items/{sku}` | Update item |
| GET | `/categories` | List item categories |
| POST | `/availability` | Multi-SKU stock check |
| POST | `/reservations` | Reserve stock for an order |
| GET | `/reservations` | List reservations (filter by order_id) |
| GET | `/reservations/{id}` | Get single reservation |
| POST | `/reservations/{id}/release` | Release reserved stock |
| POST | `/reservations/{id}/consume` | Convert reservation to consumption |
| POST | `/consumption` | Direct stock consumption (no reservation) |
| POST | `/adjustments` | Record stock adjustment |

**Warehouses:**

| Method | Path | Description |
|--------|------|-------------|
| GET | `/warehouses` | List warehouses |
| POST | `/warehouses` | Create warehouse |
| GET | `/warehouses/{id}` | Get warehouse |
| PUT | `/warehouses/{id}` | Update warehouse |
| GET | `/warehouses/{id}/locations` | List warehouse locations |
| POST | `/warehouses/{id}/locations` | Create location |

**Other modules:**

| Method | Path | Description |
|--------|------|-------------|
| GET/POST/PUT/DELETE | `/pricing-tiers` | Pricing tier management |
| GET/POST/PUT | `/transfers` | Stock transfer CRUD |
| GET/POST/PUT/DELETE | `/rbac/assignments` | RBAC role assignment |
| POST | `/media/upload` | Item asset upload |
| GET/POST/PUT/DELETE | `/modifiers` | Modifier group management |

---

## Authentication

- **JWT validation** via `shared-auth-client` v0.3.1 (JWKS from `sso.codevertexafrica.com`)
- **API key auth** for service-to-service calls (ordering-backend -> inventory-api)
- All `/v1/{tenantID}` routes are protected
- Tenant ID extracted from URL path (validated against JWT claims)

---

## Data Flow: Order Placement

```
1. Customer places order in ordering-backend
2. ordering-backend calls POST /v1/{tenant}/inventory/reservations
   - Sends order_id + list of SKUs with quantities
   - inventory-api resolves default warehouse (MAIN)
   - Transactionally: checks balance, decrements available, increments reserved
   - Returns reservation with per-item fulfillment status
3. Order progresses to completed
4. ordering-backend calls POST /v1/{tenant}/inventory/reservations/{id}/consume
   - Transactionally: decrements on_hand, decrements reserved
   - Marks reservation as "consumed"
5. If order is cancelled:
   - POST /v1/{tenant}/inventory/reservations/{id}/release
   - Restores available quantities
```

---

## Infrastructure

| Component | Config Env (uniform keys) | Default |
|-----------|----------------------------|---------|
| PostgreSQL | `POSTGRES_URL`, `POSTGRES_MAX_OPEN_CONNS`, etc. | `localhost:5432/inventory` |
| Redis | `REDIS_ADDR`, `REDIS_PASSWORD`, etc. | `localhost:6380` |
| NATS JetStream | `EVENTS_NATS_URL`, `NATS_STREAM`, etc. | `nats://localhost:4222` |
| Auth/JWKS | `AUTH_SERVICE_URL`, `AUTH_JWKS_URL`, etc. | `sso.codevertexafrica.com` |
| HTTP | `HTTP_HOST`, `HTTP_PORT`, etc. | `0.0.0.0:4003` |

---

## Migration Strategy

**Current:** Ent `Schema.Create` auto-migrate on every startup (`app.go` line 106).

**Target (MVP):** Atlas versioned migrations.
1. Generate baseline migration from current Ent schemas
2. Store migration files in `migrations/` directory
3. Run migrations via Atlas CLI in CI/CD pipeline
4. Remove auto-migrate from `app.go`

---

## Event Architecture

**Transport:** NATS JetStream (stream: `inventory`, consumer group: `inventory-workers`)

**Outbox pattern:** Mutations write an outbox row in the same DB transaction. A background publisher (`outbox.Publisher`) polls the outbox table and publishes to NATS.

**Published events (via outbox):**

| Event | Trigger |
|-------|---------|
| `inventory.stock.updated` | Any balance change (reservation, consumption, adjustment) |
| `inventory.reservation.confirmed` | Reservation created successfully |
| `inventory.reservation.released` | Reservation released |
| `inventory.stock.consumed` | Stock consumption recorded |
| `inventory.stock.low` | Available quantity drops below threshold |
| `inventory.stock.out` | Item goes to zero available |

**Consumed events:**

| Event | Action |
|-------|--------|
| `ordering.order.completed` | Auto-consume reservation |
| `ordering.order.cancelled` | Auto-release reservation |
| `pos.sale.finalized` | BOM backflush (recipe explosion + consumption) |
| `ordering.return.approved` | Restock returned items |
| `pos.return.completed` | Restock returned items |
