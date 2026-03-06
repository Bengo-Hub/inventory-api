# Inventory Service — Sprint 1: MVP Implementation

**Date:** February 16, 2026
**Sprint Goal:** Build inventory service business modules to unblock ordering-backend integration

---

## Overview

The inventory service was scaffolded with infrastructure (config, health, auth middleware, NATS, Redis) but had **zero business logic**. The ordering-backend's inventory client (`internal/platform/inventory/client.go`) expected 8 API endpoints that all returned "Not implemented yet".

This sprint implemented the full inventory business layer using Ent ORM.

---

## What Was Built

### Ent Schemas (5 entities)

| Schema | Key Fields | Indexes |
|:---|:---|:---|
| `item` | sku, name, category, price, unit_of_measure, is_active | tenant_id+sku (unique) |
| `warehouse` | name, code, address, is_default, is_active | tenant_id+code (unique) |
| `inventorybalance` | item_id (FK), warehouse_id (FK), on_hand, available, reserved | tenant_id+item_id+warehouse_id (unique) |
| `reservation` | order_id, status, items (JSON), expires_at, idempotency_key | tenant_id+order_id, idempotency_key (unique) |
| `consumption` | order_id, items (JSON), reason, status, idempotency_key | tenant_id+order_id, idempotency_key (unique) |

### Business Modules

**Items Service** (`internal/modules/items/service.go`)
- `GetStockAvailability(ctx, tenantID, sku)` — Single item availability
- `BulkAvailability(ctx, tenantID, skus)` — Bulk availability check

**Stock Service** (`internal/modules/stock/service.go`)
- `CreateReservation(ctx, tenantID, req)` — Reserve stock for an order (transactional)
- `GetReservation(ctx, tenantID, reservationID)` — Get reservation by ID
- `GetReservationsByOrderID(ctx, tenantID, orderID)` — Get reservations for an order
- `ReleaseReservation(ctx, tenantID, reservationID, reason)` — Release reserved stock
- `ConsumeReservation(ctx, tenantID, reservationID)` — Convert reservation to consumption
- `RecordConsumption(ctx, tenantID, req)` — Direct stock consumption without reservation

### HTTP Endpoints (8 total)

| # | Method | Path | Handler |
|:---|:---|:---|:---|
| 1 | GET | `/v1/{tenantID}/inventory/items/{sku}` | GetItemAvailability |
| 2 | POST | `/v1/{tenantID}/inventory/availability` | BulkAvailability |
| 3 | POST | `/v1/{tenantID}/inventory/reservations` | CreateReservation |
| 4 | GET | `/v1/{tenantID}/inventory/reservations` | GetReservationsByOrder |
| 5 | GET | `/v1/{tenantID}/inventory/reservations/{id}` | GetReservation |
| 6 | POST | `/v1/{tenantID}/inventory/reservations/{id}/release` | ReleaseReservation |
| 7 | POST | `/v1/{tenantID}/inventory/reservations/{id}/consume` | ConsumeReservation |
| 8 | POST | `/v1/{tenantID}/inventory/consumption` | RecordConsumption |

### Seed Data (`cmd/seed/main.go`)

39 menu items across 7 categories:
- Hot Beverages (10): Espresso, Latte, Cappuccino, Americano, Mocha, etc.
- Cold Beverages (7): Iced Latte, Frappes, Smoothies, Fresh Juice
- Pastries (8): Croissants, Muffins, Cake Slices, Scones, Danish
- Sandwiches (5): Club, Panini, Veggie Wrap, BLT, Tuna Melt
- Salads (2): Caesar, Greek
- Light Bites (2): Samosas, Spring Rolls
- Breakfast (4): Full English, Pancakes, Avocado Toast, Overnight Oats

All items seeded with realistic prices in KES and initial on-hand quantities.

---

## Files Created

```
internal/ent/schema/item.go
internal/ent/schema/warehouse.go
internal/ent/schema/inventorybalance.go
internal/ent/schema/reservation.go
internal/ent/schema/consumption.go
internal/ent/generate.go
internal/modules/items/service.go
internal/modules/stock/service.go
internal/http/handlers/inventory.go
cmd/seed/main.go
```

## Files Modified

```
internal/http/router/router.go    — Replaced placeholder with real routes
internal/app/app.go               — Added Ent client init, module wiring, migrations
go.mod                            — Added entgo.io/ent v0.14.5, pgx driver
```

---

## Dependencies Added

| Package | Version | Purpose |
|:---|:---|:---|
| `entgo.io/ent` | v0.14.5 | ORM framework |
| `github.com/jackc/pgx/v5/stdlib` | (transitive) | PostgreSQL driver for Ent |

## Shared Library Versions (Aligned)

| Library | Version |
|:---|:---|
| `shared-auth-client` | v0.3.1 |
| `httpware` | v0.2.0 |
| `shared-events` | v0.2.0 |

---

## Build Status

```
$ go build ./...
# No errors
```

---

## Next Steps

- [ ] Write integration tests for reservation/consumption flows
- [ ] Add NATS event publishing for stock changes
- [ ] Connect to ordering-backend's inventory client for E2E testing
- [ ] Add stock adjustment endpoints (admin use)
- [ ] Implement low-stock alerts via notifications
