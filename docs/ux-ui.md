# Inventory API - API Consumer Guide

This document is for developers integrating with inventory-api (ordering-backend, POS, future services).

**Base URL:** `https://inventoryapi.codevertexitsolutions.com/v1/{tenantID}/inventory`
**Tenant slug:** `urban-loft`

---

## Authentication

Every request must include one of:
- **Bearer token** (JWT from SSO): `Authorization: Bearer <token>`
- **API key** (service-to-service): `X-API-Key: <key>`

The `{tenantID}` in the URL path must match the tenant claim in the JWT.

---

## Endpoint Reference

### 1. GET /items/{sku} -- Single Stock Check

Returns current availability for one item.

**Request:**
```
GET /v1/{tenantID}/inventory/items/BEV-ESP-001
```

**Response (200):**
```json
{
  "item_id": "uuid",
  "sku": "BEV-ESP-001",
  "warehouse_id": "uuid",
  "on_hand": 500,
  "available": 495,
  "reserved": 5,
  "unit_of_measure": "cup",
  "updated_at": "2026-03-06T10:00:00Z"
}
```

**Errors:** `404 NOT_FOUND` if SKU does not exist or is inactive.

---

### 2. POST /availability -- Bulk Stock Check

Check availability for multiple SKUs in one call. Used by ordering-backend during cart validation.

**Request:**
```json
{
  "skus": ["BEV-ESP-001", "PST-CRO-001", "SND-CLB-001"]
}
```

**Response (200):** Array of availability objects (same shape as single check). Items not found are omitted from results.

---

### 3. POST /reservations -- Create Reservation

Reserve stock for an order. Called by ordering-backend at order placement.

**Request:**
```json
{
  "order_id": "uuid",
  "items": [
    { "sku": "BEV-ESP-001", "quantity": 2 },
    { "sku": "PST-CRO-001", "quantity": 1 }
  ],
  "expires_at": "2026-03-06T11:00:00Z",
  "idempotency_key": "order-abc-reserve-1"
}
```

`warehouse_id` is optional; defaults to the tenant's default warehouse (Busia `MAIN`).

**Response (201):**
```json
{
  "id": "reservation-uuid",
  "tenant_id": "uuid",
  "order_id": "uuid",
  "status": "pending",
  "items": [
    {
      "sku": "BEV-ESP-001",
      "requested_qty": 2,
      "reserved_qty": 2,
      "available_qty": 498,
      "is_fully_reserved": true
    },
    {
      "sku": "PST-CRO-001",
      "requested_qty": 1,
      "reserved_qty": 1,
      "available_qty": 99,
      "is_fully_reserved": true
    }
  ],
  "created_at": "2026-03-06T10:00:00Z"
}
```

**Partial reservation:** If available stock is less than requested, `reserved_qty < requested_qty` and `is_fully_reserved = false`. The reservation still succeeds (partial fill).

**Idempotency:** If `idempotency_key` matches an existing reservation, the original is returned without side effects.

---

### 4. GET /reservations?order_id={id} -- List by Order

Returns all reservations associated with an order.

---

### 5. GET /reservations/{id} -- Get Reservation

Returns a single reservation by its UUID.

---

### 6. POST /reservations/{id}/release -- Release Reservation

Releases reserved stock back to available. Called when an order is cancelled.

**Request (optional body):**
```json
{
  "reason": "order_cancelled"
}
```

**Response (200):**
```json
{
  "status": "released"
}
```

---

### 7. POST /reservations/{id}/consume -- Consume Reservation

Deducts reserved stock from on-hand. Called when an order is completed/fulfilled.

**Response (200):**
```json
{
  "status": "consumed"
}
```

This is the terminal state for a reservation. On-hand decreases, reserved decreases.

---

### 8. POST /consumption -- Direct Consumption

Records stock consumption without a prior reservation. Used for walk-in POS sales or waste tracking.

**Request:**
```json
{
  "order_id": "uuid",
  "items": [
    { "sku": "BEV-ESP-001", "quantity": 1 }
  ],
  "reason": "sale",
  "idempotency_key": "pos-txn-123"
}
```

Valid reasons: `sale`, `waste`, `adjustment`, `transfer`.

**Response (201):**
```json
{
  "id": "consumption-uuid",
  "tenant_id": "uuid",
  "order_id": "uuid",
  "status": "processed",
  "processed_at": "2026-03-06T10:05:00Z"
}
```

---

## Error Format

All errors follow a consistent structure:

```json
{
  "code": "INVALID_TENANT",
  "message": "Invalid tenant ID"
}
```

Common error codes:

| Code | HTTP Status | Meaning |
|------|-------------|---------|
| `INVALID_TENANT` | 400 | Tenant ID is not a valid UUID |
| `MISSING_SKU` | 400 | SKU parameter missing |
| `MISSING_ORDER_ID` | 400 | Order ID required |
| `MISSING_ITEMS` | 400 | Items array is empty |
| `NOT_FOUND` | 404 | Item or reservation not found |
| `RESERVATION_FAILED` | 500 | Stock reservation failed |
| `RELEASE_FAILED` | 500 | Release operation failed |
| `CONSUME_FAILED` | 500 | Consume operation failed |
| `CONSUMPTION_FAILED` | 500 | Direct consumption failed |

---

## Seed Data (Busia Outlet)

39 menu items pre-seeded for tenant `urban-loft`, warehouse `MAIN`:

| Category | Count | SKU Prefix | Example |
|----------|-------|------------|---------|
| Hot Beverages | 10 | `BEV-ESP-`, `BEV-LAT-`, `BEV-CAP-`, `BEV-AME-`, `BEV-MOC-`, `BEV-MAC-`, `BEV-TEA-`, `BEV-HOT-` | BEV-ESP-001 (Espresso, 250 KES) |
| Cold Beverages | 7 | `BEV-ICE-`, `BEV-FRP-`, `BEV-SMO-`, `BEV-JCE-` | BEV-ICE-001 (Iced Latte, 450 KES) |
| Pastries | 9 | `PST-CRO-`, `PST-MUF-`, `PST-CKE-`, `PST-DAN-`, `PST-SCO-` | PST-CRO-001 (Butter Croissant, 250 KES) |
| Sandwiches | 5 | `SND-CLB-`, `SND-GRL-`, `SND-VEG-`, `SND-BLT-`, `SND-TUN-` | SND-CLB-001 (Club Sandwich, 650 KES) |
| Salads | 2 | `SAL-CES-`, `SAL-GRK-` | SAL-CES-001 (Caesar Salad, 500 KES) |
| Light Bites | 2 | `BTE-SAM-`, `BTE-SPR-` | BTE-SAM-001 (Samosa 3pc, 300 KES) |
| Breakfast | 4 | `BRK-FUL-`, `BRK-PAN-`, `BRK-AVT-`, `BRK-OAT-` | BRK-FUL-001 (Full English, 800 KES) |

---

## Integration Notes for ordering-backend

1. **Stock check on cart validation:** Call `POST /availability` with all SKUs in the cart. Flag items where `available < requested` for the customer.
2. **Reserve on order placement:** Call `POST /reservations` with the order ID and item list. Store the `reservation.id` on the order record.
3. **Consume on order completion:** Call `POST /reservations/{id}/consume`. This is the final deduction.
4. **Release on cancellation:** Call `POST /reservations/{id}/release` with reason.
5. **Idempotency:** Always send `idempotency_key` for reservations and consumption to handle retries safely.
