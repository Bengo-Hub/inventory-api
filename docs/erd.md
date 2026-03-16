# Inventory Service – Entity Relationship Overview

The inventory service is the **single source of truth** for product master (items, units, recipes/BOM), stock levels, reservations, and consumptions for all BengoBox channels (food delivery, POS, ecommerce, logistics).  
Schemas are defined via Ent; this ERD reflects the **actual** Ent schemas in `internal/ent/schema/`.

> **Conventions**
> - UUID primary keys.
> - `tenant_id` on every table unless noted (units are core shared per CROSS-SERVICE-DATA-OWNERSHIP).
> - Timestamps use `TIMESTAMPTZ`.

---

## Actual Ent Schemas (Current)

### Product Master & Units

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `items` | `id`, `tenant_id`, `sku`, `name`, `description`, `price`, `unit_of_measure`, `is_active`, `image_url`, `metadata`, `created_at`, `updated_at` | **MASTER**: Canonical item catalogue. Unique `(tenant_id, sku)`. |
| `item_categories` | `id`, `tenant_id`, `parent_id`, `name`, `description`, `image_url`, `metadata` | **MASTER**: Hierarchical product categories. |
| `item_variants` | `id`, `item_id`, `sku`, `name`, `price_delta`, `metadata` | **MASTER**: Product variations (sizes, flavors). |
| `item_translations` | `id`, `item_id`, `locale`, `name`, `description` | **MASTER**: Localized product content. |
| `units` | `id`, `name`, `abbreviation`, `is_active`, `created_at`, `updated_at` | **MASTER**: Units of measure. Unique `name`. Core shared (no tenant_id). |
| `recipes` | `id`, `tenant_id`, `sku`, `name`, `output_qty`, `unit_of_measure`, `prep_time_minutes`, `is_active`, `metadata`, `created_at`, `updated_at` | **MASTER**: BOM/recipe header. Unique `(tenant_id, sku)`. |
| `recipe_ingredients` | `id`, `recipe_id`, `item_id`, `item_sku`, `quantity`, `unit_of_measure`, `notes`, `display_order` | **MASTER**: BOM components: ingredient item, qty per output. |

### Organisational & Balances

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `tenants` | `id`, (tenant sync fields) | Tenant registry (synced from auth / subscription context). |
| `warehouses` | `id`, `tenant_id`, `name`, `code`, `address`, `is_default`, `is_active`, `created_at`, `updated_at` | Warehouses, kitchens, outlets. Unique `(tenant_id, code)`. |
| `inventory_balances` | `id`, `tenant_id`, `item_id`, `warehouse_id`, `on_hand`, `available`, `reserved`, `unit_of_measure`, `updated_at` | Stock per item per warehouse. Unique `(tenant_id, item_id, warehouse_id)`. |

### Reservations & Consumptions

| Table | Key Columns | Description |
|-------|-------------|-------------|
| `reservations` | `id`, `tenant_id`, `order_id`, `warehouse_id`, `status` (pending/confirmed/released/consumed), `items` (JSON), `expires_at`, `confirmed_at`, `idempotency_key`, `created_at`, `updated_at` | Order-linked stock holds. Consumed by ordering-backend and POS. |
| `consumptions` | `id`, `tenant_id`, `order_id`, `warehouse_id`, `items` (JSON), `reason`, `status`, `idempotency_key`, `processed_at`, `created_at` | Stock deductions (sale, waste, adjustment). |

## Relationships (Actual)

- **items** → inventory_balances, recipe_ingredients (as ingredient), units (M2M).
- **recipes** → recipe_ingredients (recipe_id).
- **warehouses** → inventory_balances, reservations.
- **reservations** reference order_id (ordering-backend); consumptions reference order_id (ordering or POS).
- No `item_categories`, `item_boms`, `item_uoms`, `item_variants`, or `item_suppliers` in current Ent — see Roadmap.

## Roadmap (Not Yet in Ent)

| Concept | Description |
|---------|-------------|
| `item_categories` | Hierarchical product categories (tenant-scoped). Add when needed; link from items. |
| Purchasing, ledger, transfers | purchase_orders, inventory_ledger_entries, transfer_orders, etc. — add as needed. |
| Outbox / tenant_sync_events | For eventing and tenant discovery; add if not present. |

## Relationships & Interfaces

- **Entity Ownership**: This service owns all product master and inventory entities. See **shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md**. POS and ordering sync catalog as read-only cache; they do not author items/units/recipes.
- Ordering-backend and pos-api call inventory-api for: GET items, units, recipes; POST reservations; POST consumptions (or consume via events).
- Outbox/events (when implemented): `inventory.stock.updated`, `inventory.reservation.confirmed`, `inventory.stock.low`.

## Seed & Reference Data

- Sample items, units, warehouses, and recipes seeded for demo tenants.
- Default reason codes for consumption: `sale`, `waste`, `adjustment`, `transfer`.

---

Update this ERD whenever Ent schemas change. Run `go generate ./internal/ent` and refresh downstream documentation to keep integrations aligned.

