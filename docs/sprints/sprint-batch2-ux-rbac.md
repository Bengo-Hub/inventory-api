# Sprint: Batch 2 — UX Revamp, RBAC Fix & ERP Polish

**Created:** 2026-05-27  
**Status:** ✅ Complete — all 8 issues resolved, both services built and pushed  
**Goal:** Close UX gaps and critical bugs surfaced after Batch 1 MVP launch

---

## Issues Addressed

| # | Area | Issue | Resolution |
|---|------|-------|------------|
| 1 | **Recipes 404** | All recipe CRUD failed with 404 due to wrong URL prefix in `inventory-ui/src/lib/api/recipes.ts` (`/api/v1/tenants/` instead of `/api/v1/`) | Fixed URL in all 5 recipe API functions |
| 2 | **Modifiers 403** | Demo tenant admin (role: `admin`) got "insufficient permissions" on all guarded endpoints | Added `admin`, `manager`, `super_admin`, `store_manager` to RBAC bypass list in `rbac.go` |
| 3 | **Adjustments UX** | Two-tab layout hid history; form was primary view | Revamped: history table as landing page, `+ New Adjustment` opens modal |
| 4 | **Transfers modal** | Modal too small; item search dropdown overflowed behind modal boundary; no qty shown | Expanded to `max-w-3xl`; `ItemSearchInput` got `fixedDropdown` prop using `getBoundingClientRect`; available qty shown below each row |
| 5 | **Lots creation** | Supplier Reference was a plain text input; no helper texts | Replaced with searchable `SupplierRefCombobox` (searches suppliers endpoint); added helper texts to all 4 fields |
| 6 | **Stock levels** | No per-row actions; no detail view | Added `SlidersHorizontal` action button + `StockDrawer` (Sheet) with stats grid, inline adjustment form, and recent adjustments history |
| 7 | **Units page** | Type column always "—"; Used-by count always 0; no detail view | Backend: `type` field added to schema + Atlas migration; `item_count` via batch COUNT query. Frontend: Eye button opens Sheet drawer with type + linked items |
| 8 | **Categories** | No parent-child support; catalog used hardcoded filter pills | Backend: `ListCategories` returns `parent_id`/`parent_name`; create/update persist `parent_id`. Frontend: parent select in modal, `└─` hierarchy in table, catalog pills use `useCategories()` |

---

## Backend Changes (inventory-api)

### Schema / Migration
- `internal/ent/schema/unit.go` — added `type` optional string field
- `internal/ent/migrate/migrations/20260527144311_add_unit_type.sql` — `ALTER TABLE units ADD COLUMN type VARCHAR NULL`
- Ent code regenerated (unit.go, unit_create.go, unit_update.go, unit/where.go, mutation.go, runtime.go)

### Services
- `internal/modules/units/service.go`
  - `UnitDTO` extended: `Type string`, `ItemCount int`
  - `ListUnits`: batch COUNT query via `item.UnitIDIn(...)` to populate `item_count`
  - `CreateUnit` / `UpdateUnit`: persist `type` field
- `internal/modules/items/service.go`
  - `CategoryDTO` extended: `ParentName string`; `ParentID` now always set
  - `ListCategories`: populates `parent_id` + `parent_name` from in-memory name map
  - `CreateCategory` / `UpdateCategory`: accept and persist `parent_id`
  - `ListItems`: new params `categoryID *uuid.UUID`, `unitID *uuid.UUID`, `search string`; filters applied in `buildQuery()`

### Middleware
- `internal/http/middleware/rbac.go` — both `RequirePermission` and `RequireAnyPermission` now loop through `["inventory_admin", "admin", "manager", "super_admin", "store_manager"]` for bypass

### Handler Interface
- `ItemsServicer.ListItems` signature updated to accept `categoryID *uuid.UUID`, `unitID *uuid.UUID`, `search string`
- `ListItems` handler parses `?category_id=`, `?unit_id=`, `?search=` query params

### Seed
- `cmd/seed/main.go` — `seedSuppliers()` creates "Demo Distributor Co." for `codevertex-demo`; `seedReorderConfig()` enables auto-reorder on 3 items with that supplier

---

## Frontend Changes (inventory-ui)

### New Components
- `src/components/ui/sheet.tsx` — `Sheet`, `SheetHeader`, `SheetTitle`, `SheetContent` — reusable right-side slide-in panel with Escape key support and `animate-in slide-in-from-right` animation

### Modified Components
- `src/components/inventory/ItemSearchInput.tsx` — `fixedDropdown?: boolean` prop: when true, uses `useLayoutEffect` + `getBoundingClientRect` to render dropdown with `position:fixed` so it escapes `overflow:hidden` modal parents

### Pages Revamped
| Page | Changes |
|------|---------|
| `adjustments/page.tsx` | Removed tabs; history table is default view; `AdjustmentModal` opened by "+ New Adjustment" button |
| `transfers/page.tsx` | `max-w-3xl` modal; `overflow-y-auto` on card body only; `fixedDropdown` on `ItemSearchInput`; available qty displayed |
| `lots/page.tsx` | `SupplierRefCombobox` replaces plain text input; helper texts on all 4 fields |
| `stock/page.tsx` | `SlidersHorizontal` action button + `StockDrawer` (Sheet): stats grid, toggle inline adjustment form, recent adjustments list |
| `units/page.tsx` | Eye button → `UnitDrawer`: type label, linked items list (via `useItems({ unit_id })`) |
| `categories/page.tsx` | Parent select in create/edit modal; Parent column in table; hierarchical `└─` sort; root categories shown first |
| `catalog/page.tsx` | Category filter pills replaced: `useCategories()` data → `category_id` passed to `useItems` |

### API / Hooks
| File | Change |
|------|--------|
| `lib/api/recipes.ts` | Fixed URL: `/api/v1/tenants/${orgSlug}/` → `/api/v1/${orgSlug}/` |
| `lib/api/items.ts` | `list()` params extended: `unit_id?`, `category_id?` |
| `lib/api/units.ts` | `Unit` interface: added `item_count?: number` |
| `lib/api/categories.ts` | `Category`: added `parent_id?`, `parent_name?`; added `CategoryPayload`, `create()`, `update()` |
| `hooks/useItems.ts` | `useItems()` params extended: `unit_id?`, `category_id?` |

---

## Verification Checklist

- [x] `go build ./...` — zero errors
- [x] `pnpm build` — zero TypeScript errors, all 27 routes compiled
- [x] Atlas migration `20260527144311_add_unit_type.sql` generated and committed
- [x] inventory-api pushed to `main` (commit `d32b7f2`)
- [x] inventory-ui pushed to `master` (commit `4f65b8f`)

---

## References

- [RBAC and Seed](../rbac-and-seed.md)
- [ERD](../erd.md)
- [UX/UI Consumer Guide](../ux-ui.md)
