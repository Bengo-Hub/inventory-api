# Inventory API - Architecture

**Service:** inventory-api
**Language:** Go 1.22+
**ORM:** Ent (entgo.io/ent v0.14.5)
**HTTP Router:** chi/v5
**Port:** 4003
**Production:** `inventoryapi.codevertexitsolutions.com`

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

## Ent Schemas (7 entities)

| Schema | Purpose | Key Indexes |
|--------|---------|-------------|
| `item` | Canonical SKU catalogue | `(tenant_id, sku)` unique |
| `warehouse` | Physical storage locations | `(tenant_id, code)` unique |
| `inventorybalance` | Stock levels per item per warehouse | `(tenant_id, item_id, warehouse_id)` unique |
| `reservation` | Order-level stock reservations | `(tenant_id, order_id)`, `idempotency_key` unique |
| `consumption` | Stock deduction records | `(tenant_id, order_id)`, `idempotency_key` unique |
| `recipe` | BOM header: maps menu-item SKU to a recipe | `(tenant_id, sku)` unique |
| `recipeingredient` | BOM line: links recipe to raw inventory items | `(recipe_id, item_id)` unique |

---

## HTTP Endpoints

All routes are mounted under `/v1/{tenantID}/inventory/`.

| # | Method | Path | Handler | Description |
|---|--------|------|---------|-------------|
| 1 | GET | `/items/{sku}` | GetStockAvailability | Single item stock check |
| 2 | POST | `/availability` | BulkAvailability | Multi-SKU stock check |
| 3 | POST | `/reservations` | CreateReservation | Reserve stock for an order |
| 4 | GET | `/reservations?order_id={id}` | GetReservationsByOrder | List reservations by order |
| 5 | GET | `/reservations/{id}` | GetReservation | Get single reservation |
| 6 | POST | `/reservations/{id}/release` | ReleaseReservation | Release reserved stock |
| 7 | POST | `/reservations/{id}/consume` | ConsumeReservation | Convert reservation to consumption |
| 8 | POST | `/consumption` | RecordConsumption | Direct stock consumption (no reservation) |

---

## Authentication

- **JWT validation** via `shared-auth-client` v0.3.1 (JWKS from `sso.codevertexitsolutions.com`)
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
| NATS JetStream | `NATS_URL`, `NATS_STREAM`, etc. | `nats://localhost:4222` |
| Auth/JWKS | `AUTH_SERVICE_URL`, `AUTH_JWKS_URL`, etc. | `sso.codevertexitsolutions.com` |
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

**Planned events (MVP):**

| Event | Trigger |
|-------|---------|
| `inventory.stock.updated` | Any balance change (reservation, consumption, adjustment) |
| `inventory.reservation.confirmed` | Reservation created successfully |
| `inventory.stock.low` | Available quantity drops below threshold |

**Consumed events (post-MVP):**

| Event | Action |
|-------|--------|
| `auth.tenant.created` | Initialize tenant in inventory |
| `cafe.order.completed` | Auto-consume reservation |
| `cafe.order.cancelled` | Auto-release reservation |
