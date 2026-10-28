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
| `handlers/media.go` | Single image upload (create-mode item image, brand and supplier logos, `?type=`), stored on the shared object storage |
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
| GET/POST/PUT | `/transfers` | Stock transfer CRUD. The list takes `status` as one value or a comma list (`draft,in_transit` lists every transfer not yet received) plus `search`, `from`, `to`, `page`, `limit` |
| GET/POST/PUT/DELETE | `/rbac/assignments` | RBAC role assignment |
| POST | `/media/upload` | Item asset upload |
| GET/POST/PUT/DELETE | `/modifiers` | Modifier group management |

---

## Purchase cost and price on receipt (2026-10-12)

Posting a goods receipt (`postGoodsReceiptCore`, also behind PO **Mark Received**) creates one cost
layer per accepted line and then calls `stock.RecomputeStandardCost` once per item:

- `cost_price` (per stock unit) becomes the newest layer's cost for fifo/lifo/fefo tenants, or the
  quantity weighted average of open layers for wavg tenants. COGS and stock value still read each
  layer's own cost, so stock on hand is never revalued.
- `purchase_price` (price per purchase pack) follows the newest layer: unit cost times
  `purchase_pack_size`. The item form, the catalog cost column and the PO "last paid" prefill read
  `purchase_price` first, so before this it kept showing the old buying price after a new purchase.
  Items that never stored a purchase price keep reading `cost_price`.
- A selling price change rides on the line (`new_selling_price`, `price_scope`). Mark Received now
  copies both from the PO line into the auto-created receipt line; it used to drop them.

## Variants (2026-10-28)

Retail products sold in several sizes and colours use child items. Each variant is a normal
`Item` with its own SKU, barcode, price, cost and stock, so balances, receipts, transfers, counts,
lots, reservations, pricing tiers, eTIMS registration and COGS all work for it with no special
case. The link is stored in metadata, so there is no schema change:

- child item: `metadata.variant_of` is the parent item id and `metadata.variant_attributes` holds
  its values, for example `{"size":"M","colour":"Red"}`.
- parent item: `metadata.variant_axes` maps each axis to its ordered values, for example
  `{"colour":["Red","Blue"],"size":["S","M"]}`. The parent is the display product and holds no
  stock of its own. Nothing blocks stock on it; the screens simply sell and count the children.

The older `ItemVariant` table stays for price-only variations that share the parent's stock. Reads
fall back to it whenever an item has no child items. The code lives in
`internal/modules/items/variant_children.go`.

### Endpoints

- `PUT /{tenant}/inventory/items/{itemId}/variants` (permission `inventory.variants.change`)
  takes the whole matrix, `{"axes":{"size":["S","M"],"colour":["Red","Blue"]},"rows":[{"attributes":{"size":"M","colour":"Red"},"sku":"","barcode":"","price":1200,"is_active":true}]}`.
  Every row must give a value for each axis, from that axis's list, and no combination may
  repeat. A row whose attributes match an existing child updates it. A new row creates a child
  that copies the parent's type, category, unit, brand, tax code, eTIMS codes, description,
  use case, tracking mode and purchase fields. Its name is "T-shirt, Red / M" (values in
  alphabetical axis order) and its SKU defaults to the parent SKU plus the upper-cased values
  ("TSHIRT-RED-M"). A row price goes through the same path as `selling_price` on item create,
  so it becomes the Retail and Wholesale prices; without one the child inherits the parent's
  prices. Children left out of the body are deactivated, never deleted. The parent's
  `variant_axes` is merged into its metadata and `item.updated` is published for the parent;
  children publish `item.created` or `item.updated` through the normal create and update code.
  The call is not one transaction, but matching by attributes makes a retry safe. Errors are 400
  for bad rows, 404 for an unknown parent and 409 for a SKU another item already holds. The
  response is every child, active or not, as `VariantDTO`.
- `GET /{tenant}/inventory/items/{itemId}/variants` returns the active children, or the legacy
  `ItemVariant` rows when there are none.
- `VariantDTO` is `{id, sku, name, price, attributes, barcode, is_active, image_url, on_hand, available}`.
  For a child, `id` is the child item id and `price` its effective selling price.

### Listing and lookups

- `GET /items?include=variants` fills `variants[]` and `has_variants` on every parent of the page
  from its children in one query (`metadata->>'variant_of' IN (...)`, tenant scoped), with stock
  summed over the same outlet or warehouse scope as the row itself.
- The same request hides the children as rows, because the storefront and POS catalog proxies
  always send `include=variants` and show children inside the parent. `include_children=1` keeps
  them as rows. Without `include=variants` (stock pages, receipts, counts, transfers, the bulk
  import) and for explicit lookups (`id`, `skus`, `search`), children are always listed, so a
  scanned child barcode or SKU resolves.
- Every `ItemDTO` carries read-only `variant_of` and `variant_attributes` when it is a child, and
  `GET /items/{sku}` returns the same two fields on its availability response.
- `ResolveItemBySKU` and `resolveStockSKU` in the stock module now prefer an item with the exact
  SKU and only then try a legacy `ItemVariant` SKU, taking the oldest match instead of failing
  when two parents share a variant SKU.

### Bulk import

The Items sheet (and CSV) accepts two optional columns. `variant_of_sku` names the parent, which
must already exist or appear earlier in the sheet, and `variant_attributes` lists the values as
`size=M;colour=Red`. Both must be filled for a variant row. After the sheet is processed each
touched parent's `variant_axes` is rebuilt from its active children, keeping the existing value
order. Re-importing a row now merges the stored metadata instead of wiping it.

### Index

No index backs `metadata->>'variant_of'`. Every lookup is scoped by the indexed `tenant_id`, which
keeps the scanned set to one tenant's items. If it shows up in slow query logs, add an expression
index through the Ent and Atlas versioned migration workflow.

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
| `inventory.stock.out` | Item goes to zero available (alert). `affects_availability` says whether consumers may mark it unavailable: false unless the tenant enabled `auto_hide_on_stock_out` (see docs/recipes.md) |
| `inventory.stock.in` | Recipe producible again after an ingredient restock; only published for `auto_hide_on_stock_out` tenants |

**Consumed events:**

| Event | Action |
|-------|--------|
| `ordering.order.completed` | Auto-consume reservation |
| `ordering.order.cancelled` | Auto-release reservation |
| `pos.sale.finalized` | BOM backflush (recipe explosion + consumption) |
| `ordering.return.approved` | Restock returned items |
| `pos.return.completed` | Restock returned items |

## Media storage (2026-10-17)

Images live on the shared platform object storage (Cloudflare R2 through `httpware/objectstore`,
see `devops-k8s/docs/object-storage.md`), public bucket, served from `https://media.codevertexafrica.com`.

- Keys: `inventory/<tenant_id>/<type>/...` with `item-images` (items, gallery, variants), `logos`
  (brands, suppliers) and `images` (uploaded category pictures). Rows store the key; every read
  (item, gallery, category, brand, supplier DTOs and the `item.created` / `item.updated` events)
  turns it into the public link, so consumers keep a full URL.
- Uploads: `POST /{tenant}/inventory/items/{id}/images` (gallery) and `POST /api/v1/media/upload?type=`
  (create-mode item image, logos). JPEG or PNG, checked from the bytes and re-encoded (EXIF removed).
  A form may send back the link it was given; writes normalise it to the key.
- Storage only keeps files in use: replacing or removing an item image, deleting a gallery image or
  deleting an item frees its files, and replacing a brand or supplier logo frees the old logo, each
  once no other row of the tenant uses the file. The file is deleted right after the row is saved,
  so a failed save never leaves a row on a deleted file.
- Shipped platform assets stay static files of the image, served at `/media`: the category icons
  (`/media/icons/*.svg`) and the demo menu pictures the seeds use (`/media/images/outlets/menu/*`,
  shipped since 2026-10-17; before that the seeded demo items pointed at files that were never
  deployed and showed broken images).
- A change of an item's primary picture (first upload, set primary, deleting the primary) updates
  `items.image_url` and writes `item.updated` in the same transaction, so the POS catalogue cache,
  terminals and the storefront refresh the picture (2026-10-27; before, gallery changes sent no event).
- Bulk images (2026-10-27): `POST /{tenant}/inventory/items/images/match` takes up to 100 file names
  and suggests an item for each: a file named after a SKU or barcode first, then an exact name, then
  the most similar names through the `item_name` trigram index, with alternatives. It reads only and
  needs `items.change`. The inventory-ui "Upload images" dialog compresses the photos in the browser,
  shows the pairs for review, then sends each file through the normal gallery upload, so plan gates,
  re-encoding and storage rules stay in one place.
- Imports keep pictures: a CSV or XLSX re-import updates existing items with the whole row, and rows
  carry no image, so the import now sends each item's current image back (`keepImageOnImport`).
  Before 2026-10-27 a re-import erased every image uploaded since.
- Existing files: `inventory-relink-media` (report) and `inventory-relink-media --apply` inside the pod
  move every file a row still references by an old `/media/...` path or `MEDIA_URL_BASE/media/...` URL to
  its tenant's key, update the row and publish `item.updated` for items. Unreferenced files are not
  moved. Until a row is relinked its old path keeps being served from the media volume.
