# RBAC and Seed (inventory-api)

**Last updated**: March 2026

## RBAC: DB-backed Permission/Role schema

inventory-api maintains its own RBAC tables following the same pattern as treasury-api. The system uses Ent ORM schemas under `internal/ent/schema/` and a DB-backed RBAC module at `internal/modules/rbac/`.

### Schemas

| Schema | Table | Description |
|--------|-------|-------------|
| `InventoryPermission` | `inventory_permissions` | Permission definitions (global, no tenant_id) |
| `InventoryRole` | `inventory_roles` | Role definitions (tenant-scoped) |
| `RolePermission` | `role_permissions` | Junction table: role <-> permission |
| `UserRoleAssignment` | `user_role_assignments` | User <-> role assignments (tenant-scoped) |
| `InventoryUser` | `inventory_users` | Local user reference (synced from auth-service) |
| `RateLimitConfig` | `rate_limit_configs` | Rate limiting configuration |
| `ServiceConfig` | `service_configs` | Service-level key-value configuration |

### Permission format

Permission codes follow the `inventory.{module}.{action}` pattern.

**Modules** (11):
- `items` -- catalog/SKU management
- `variants` -- item variant management
- `categories` -- item category management
- `warehouses` -- warehouse and location management
- `stock` -- stock adjustments, cycle counts, transfers
- `recipes` -- recipe/BOM management
- `consumptions` -- stock consumption tracking
- `reservations` -- inventory reservations and allocation
- `units` -- unit of measure management (**platform-only for manage operations** since units have no tenant_id)
- `config` -- service configuration management
- `users` -- user management

**Actions** (9):

| Action       | Description                          |
|-------------|--------------------------------------|
| `add`       | Create new records                   |
| `view`      | View any record                      |
| `view_own`  | View own/tenant-scoped records only  |
| `change`    | Update any record                    |
| `change_own`| Update own/tenant-scoped records only|
| `delete`    | Delete/cancel records                |
| `delete_own`| Delete own records only              |
| `manage`    | Full management (all of the above)   |
| `manage_own`| Full management of own scope only    |

Example permission codes: `inventory.items.view`, `inventory.stock.add`, `inventory.warehouses.change`, `inventory.units.manage`.

### Roles (seeded per tenant)

| Role Code | Description |
|-----------|-------------|
| `inventory_admin` | Full access to all inventory operations including config and user management |
| `warehouse_manager` | Manage warehouses, stock, reservations, recipes, and consumptions |
| `stock_clerk` | View and change stock, view items/warehouses, manage own consumptions |
| `viewer` | Read-only access to all inventory data |

### API Endpoints

All RBAC endpoints are under `/{tenant}/` and require authentication:

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/{tenant}/rbac/roles` | List roles for tenant |
| `GET`  | `/{tenant}/rbac/permissions` | List all permissions |
| `GET`  | `/{tenant}/rbac/assignments` | List role assignments |
| `POST` | `/{tenant}/rbac/assignments` | Assign role to user |
| `DELETE`| `/{tenant}/rbac/assignments/{id}` | Revoke role assignment |
| `GET`  | `/{tenant}/users/me/roles` | Get current user's roles |
| `GET`  | `/{tenant}/users/me/permissions` | Get current user's permissions |

## Seed

Run `go run ./cmd/seed` to seed:

1. **Units** (global, no tenant_id)
2. **Warehouses, categories, items, balances** (tenant-scoped for urban-loft)
3. **Permissions** (99 permissions: 11 modules x 9 actions)
4. **Roles** (4 system roles per tenant)
5. **Role-permission assignments** (admin gets all, others get subsets)
6. **Rate limit configs** (global, per-tenant, per-IP, per-user, per-endpoint)
7. **Service configs** (platform-level defaults)

### Important: `units` module

The `unit` entity has **no tenant_id** -- it is global/shared across all tenants. Only platform owners should have `inventory.units.manage` permission. The `warehouse_manager` and `stock_clerk` roles only get `inventory.units.view`.

## Module structure

```
internal/modules/rbac/
  models.go          -- InventoryUser, InventoryRole, InventoryPermission, UserRoleAssignment
  repository.go      -- Repository interface + filter types
  repository_ent.go  -- Ent-backed implementation
  service.go         -- Business logic (HasPermission, AssignRole, SyncUser, etc.)
```

## References

- Treasury-api reference: `finance-service/treasury-api/internal/modules/rbac/` (same pattern)
- Auth-api seed: `auth-service/auth-api/cmd/seed`
- Ordering-backend tenant sync: `ordering-service/ordering-backend/cmd/seed`
- inventory-ui: Sidebar and route visibility use permission codes like `inventory.items.view`
