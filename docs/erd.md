# Inventory Service – Entity Relationship Overview

The inventory service is the **single source of truth** for:
- **Item/product master data** — all types: physical goods, services, recipes/BOM, ingredients, vouchers, equipment
- **Stock levels** per item per outlet/location
- **Reservations** (holds during order placement)
- **Consumptions** (deductions on order completion or waste)

This serves **all business channels**: food delivery, POS, e-commerce, hospitality, retail, grocery, pharmacy, electronics, etc.

Schemas are defined via Ent; this ERD reflects the **actual** Ent schemas in `internal/ent/schema/`.

> **Conventions**
> - UUID primary keys.
> - `tenant_id` on every table (except `units` — shared globally).
> - Timestamps use `TIMESTAMPTZ`.
> - Unique `(tenant_id, sku)` on items and recipes.

---

## Product Master — Item Catalogue

The `items` table is **use-case agnostic** — it stores any type of product or service via the `type` field.

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `items` | `id`, `tenant_id`, `sku` (UNIQUE per tenant), `name`, `description`, `category_id`, `unit_id`, `type`, `is_active`, `image_url`, `metadata`, `created_at`, `updated_at` | **MASTER**: Canonical item catalogue. `type` determines behavior. |
| `item_categories` | `id`, `tenant_id`, `name`, `description`, `is_active`, `created_at`, `updated_at` | Hierarchical categories (tenant-scoped). Unique `(tenant_id, name)`. |
| `item_assets` | `id`, `item_id`, `asset_type` (IMAGE/VIDEO/DOCUMENT/3D_MODEL), `url`, `file_name`, `file_size`, `mime_type`, `metadata`, `display_order`, `is_primary`, `created_at`, `updated_at` | Media assets per item. Multiple per item with display ordering. |
| `item_variants` | `id`, `item_id`, `sku` (UNIQUE per item), `name`, `price`, `is_active`, `created_at`, `updated_at` | Product variations: sizes, colors, pack sizes, dosage forms. Unique `(item_id, sku)`. |
| `item_translations` | `id`, `item_id`, `locale`, `name`, `description`, `created_at`, `updated_at` | Localized names/descriptions. Unique `(item_id, locale)`. |
| `units` | `id`, `name` (UNIQUE globally), `abbreviation`, `is_active`, `created_at`, `updated_at` | Units of measure: KG, GRAM, PIECE, LITRE, ML, CUP, SERVING, BOX, etc. **No `tenant_id`** — shared across all tenants. |

### Item Type Enum

| Type | Stock Tracked? | Has Recipe/BOM? | Use Case Examples |
|------|---------------|-----------------|-------------------|
| `GOODS` | ✅ Yes | No (unless recipe) | Coffee beans, grocery items, electronics, apparel |
| `SERVICE` | ❌ No | No | Delivery fee, consulting hour, haircut |
| `RECIPE` | ✅ Yes (finished good) | ✅ Yes | Cappuccino, pasta dish, smoothie, assembled product |
| `INGREDIENT` | ✅ Yes (raw stock) | No | Espresso beans, flour, milk, chemical compound |
| `VOUCHER` | Optional | No | Gift card, discount voucher, prepaid credit |
| `EQUIPMENT` | ❌ No | No | Blender, coffee machine, display stand |

### Multi-Use-Case Item Examples

| Business Type | Category | Item (Type) | SKU Pattern |
|--------------|----------|-------------|-------------|
| Café | Hot Beverages | Cappuccino (`RECIPE`) | `BEV-CAP-001` |
| Café | Ingredients | Espresso Beans (`INGREDIENT`) | `ING-ESP-001` |
| Grocery | Fresh Produce | Avocado (`GOODS`) | `GRC-AVO-001` |
| Electronics | Phones | iPhone 15 Pro (`GOODS`) | `ELC-APL-001` |
| Pharmacy | OTC Medication | Paracetamol 500mg (`GOODS`) | `PHR-PAR-001` |
| Restaurant | Mains | Grilled Salmon (`RECIPE`) | `MNU-SAL-001` |
| Retail | Apparel | Blue T-Shirt L (`GOODS`) | `APR-TSH-L001` |

---

## Recipe / Bill of Materials (BOM)

Recipes link a finished item (SKU) to its raw ingredient components. This enables:
- **Ingredient-level stock deduction** on order completion
- **Availability checking** (can we make this dish given current ingredient stock?)
- **Food costing** and waste tracking

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `recipes` | `id`, `tenant_id`, `sku` (UNIQUE per tenant — matches finished item SKU), `name`, `output_qty`, `unit_of_measure`, `is_active`, `prep_time_minutes`, `metadata`, `created_at`, `updated_at` | BOM header. `sku` must match the `RECIPE`-typed item's SKU. |
| `recipe_ingredients` | `id`, `recipe_id`, `item_id` (FK to ingredient item), `item_sku` (denormalized), `quantity`, `unit_of_measure`, `notes`, `display_order` | BOM line items. One per ingredient. Unique `(recipe_id, item_id)`. |

### Recipe Example (Cappuccino)

```
Recipe { sku: "BEV-CAP-001", output_qty: 1, unit: "cup" }
  └── RecipeIngredient { item_sku: "ING-ESP-BEANS", qty: 2, unit: "shot" }
  └── RecipeIngredient { item_sku: "ING-MILK",      qty: 1, unit: "cup" }
  └── RecipeIngredient { item_sku: "ING-STEAM-MILK", qty: 0.5, unit: "cup" }
```

---

## Outlet / Location Management

The `warehouses` table represents **any physical location** where stock is held — warehouse, store, restaurant kitchen, supermarket branch, pharmacy outlet, etc.

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `warehouses` | `id`, `tenant_id`, `name`, `code` (UNIQUE per tenant), `address`, `is_default`, `is_active`, `created_at`, `updated_at` | Physical stock location. `code = "MAIN"` for the default/HQ location. Maps to outlet/branch concept. |

### Outlet Naming Conventions by Use Case

| Use Case | "Warehouse" represents |
|----------|----------------------|
| Café / Restaurant | Kitchen / Branch |
| Supermarket | Store / Branch |
| Pharmacy | Dispensary / Branch |
| Warehouse / Manufacturing | Warehouse / Plant |
| E-commerce | Fulfillment Center |

> **Note**: The Ent entity is named `Warehouse` for historical reasons but semantically represents any **outlet/location** that holds or dispatches stock. A future rename to `StockLocation` or `Outlet` may be considered but schema migration is not required now — the `code` and `name` fields provide the semantic label.

---

## Stock Management

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `inventory_balances` | `id`, `tenant_id`, `item_id`, `warehouse_id`, `on_hand`, `available` (on_hand - reserved), `reserved`, `unit_of_measure`, `updated_at` | Atomic stock per item per location. Unique `(tenant_id, item_id, warehouse_id)`. |

### Stock Formula

```
available = on_hand - reserved

On reservation:   available -= qty, reserved += qty
On consumption:   on_hand  -= qty, reserved -= qty
On release:       available += qty, reserved -= qty
```

---

## Reservations & Consumptions

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `reservations` | `id`, `tenant_id`, `order_id`, `warehouse_id` (defaults to tenant default), `status` (pending/confirmed/released/consumed), `items` (JSON array), `expires_at`, `confirmed_at`, `idempotency_key` (UNIQUE), `created_at`, `updated_at` | Stock hold for an order. `items` contains per-SKU `reserved_qty`/`available_qty`/`is_fully_reserved`. Idempotency prevents duplicate holds. |
| `consumptions` | `id`, `tenant_id`, `order_id`, `warehouse_id`, `items` (JSON array of `{sku, quantity}`), `reason` (sale/waste/adjustment/transfer), `status`, `idempotency_key` (UNIQUE), `processed_at`, `created_at` | Immutable stock deduction record. Idempotency prevents double-deductions. |

### Stock Flow for Order Placement

```
1. ordering-backend POST /reservations  → inventory creates Reservation (available -= qty)
2. Payment confirmed                    → ordering-backend marks order paid
3. ordering-backend POST /reservations/{id}/consume → inventory creates Consumption (on_hand -= qty, reserved -= qty)
   └── If item is RECIPE: explode BOM, deduct each ingredient separately
4. Order cancelled                      → ordering-backend POST /reservations/{id}/release (available += qty)
```

---

## Tenant Registry (Synced)

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `tenants` | `id`, `name`, `slug` (UNIQUE), `status`, `use_case`, `subscription_plan`, `subscription_status`, `tier_limits`, `metadata`, `created_at`, `updated_at` | Local copy synced from auth-service via JIT provisioning on first request. `use_case` hints: hospitality, retail, quick_service, manufacturing, warehousing, services, e_commerce, other. |

---

## Event Infrastructure

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `outbox_events` | `id`, `tenant_id`, `aggregate_type`, `aggregate_id`, `event_type`, `payload`, `status` (PENDING/PUBLISHED/FAILED), `attempts`, `last_attempt_at`, `published_at`, `error_message`, `created_at` | Transactional outbox. Events published to NATS: `inventory.stock.updated`, `inventory.reservation.confirmed`, `inventory.stock.low`. |

---

## Entity Relationships

```
tenants.id ← warehouses.tenant_id
tenants.id ← items.tenant_id
tenants.id ← item_categories.tenant_id
tenants.id ← recipes.tenant_id

items.id ← inventory_balances.item_id
items.id ← item_assets.item_id
items.id ← item_variants.item_id
items.id ← item_translations.item_id
items.id ← recipe_ingredients.item_id  (ingredient reference)
items.category_id → item_categories.id
items.unit_id → units.id

recipes.id ← recipe_ingredients.recipe_id
recipes.sku == items.sku (RECIPE type)   ← SKU must match

warehouses.id ← inventory_balances.warehouse_id
warehouses.id ← reservations.warehouse_id

reservations.order_id → ordering-backend / pos-api (external reference)
consumptions.order_id → ordering-backend / pos-api (external reference)
```

---

## Multi-Tenant & Multi-Outlet Stock

```
Tenant A (Urban Loft Café) — use_case: hospitality
  ├── Warehouse { code: "MAIN", name: "Busia Kitchen" }
  │     ├── InventoryBalance { item: Espresso Beans, on_hand: 500, available: 450 }
  │     └── InventoryBalance { item: Milk, on_hand: 200, available: 180 }
  └── (future) Warehouse { code: "NAIROBI", name: "Nairobi Branch" }

Tenant B (SuperMart) — use_case: retail
  ├── Warehouse { code: "MAIN", name: "Westlands Store" }
  │     └── InventoryBalance { item: iPhone 15, on_hand: 30, available: 28 }
  └── Warehouse { code: "MOMBASA", name: "Mombasa Branch" }
```

---

## Seed Data Overview

- **Demo Tenant**: `urban-loft` — UUID synced from auth-service
- **Demo Outlet**: `warehouses { code: "MAIN", name: "Urban Loft Busia Kitchen" }`
- **39 demo items** covering 7 categories: hot-beverages, cold-beverages, pastries, sandwiches, salads, light-bites, breakfast
- **Units**: KG, GRAM, PIECE, LITRE, ML, CUP, SERVING, BOX, BOTTLE, etc.
- **Opening balances**: per item in MAIN warehouse
- **Future seed expansion**: Electronics (phones, accessories), Grocery (fresh produce, dairy), Pharmacy items, Retail apparel — all supported by the same flexible schema

> Item/category seeding is **owned by inventory-api**. Downstream services (ordering-backend, pos-api) should NOT seed items — they pull from inventory-api via API or sync events.

---

> Update this ERD whenever Ent schemas change. Run `go generate ./internal/ent` after schema changes, then generate Atlas versioned migration with `go run ent/migrate/main.go <migration_name>`.
