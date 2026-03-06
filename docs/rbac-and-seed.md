# RBAC and Seed (inventory-api)

**Last updated**: March 2026

## RBAC: No local Permission/Role schema

inventory-api **does not** maintain its own Role or Permission tables. Authorization is enforced via **auth-api JWT** using `shared-auth-client`: all protected routes require a valid Bearer token; tenant and user context come from JWT claims.

For inventory resources, auth-api is the source of truth for roles and permissions. When defining permissions in auth-api for inventory-api consumers, use the standard **eight actions** per resource:

| Action     | Description                          |
|-----------|--------------------------------------|
| `add`     | Create new records                   |
| `read`    | View any record                      |
| `read_own`| View own/tenant-scoped records only  |
| `change`  | Update any record                    |
| `change_own` | Update own/tenant-scoped records only |
| `delete` | Delete/cancel records                |
| `manage`  | Full management (all of the above)   |
| `manage_own` | Full management of own scope only |

**Suggested inventory resources** (for use in auth-api permission seeds):

- **items** — catalog/SKU management
- **warehouses** — warehouse and location management
- **adjustments** — stock adjustments, cycle counts
- **transfers** — inter-warehouse transfers
- **purchase_orders** — purchase orders and receipts
- **reservations** — inventory reservations and allocation

Example permission codes: `items:read`, `items:add`, `warehouses:change`, `adjustments:manage`, etc.

## Core data: no local seed

- **No cmd/seed in inventory-api**: This service does not have a `cmd/seed` binary. Do not add a local seed for tenants or roles/permissions; align with auth-api (permissions) and ordering-backend (tenant sync) instead.
- **Tenants**: Tenants are **synced from ordering-backend** (inventory-api is listed in `tenantSyncDestinations` in `ordering-service/ordering-backend/cmd/seed/main.go` and `internal/modules/identity/repository_ent.go`). Do not duplicate tenant seed in inventory-api.
- **Warehouses / outlets**: When warehouse/outlet tables exist, they are populated by **tenant sync events** or by API usage; no standalone warehouse seed is required in inventory-api for MVP.
- **Default config**: Document any service-level defaults in `config/example.env`. No migration files are added manually; use existing migration tooling when schema is introduced.

## References

- Auth-api seed: `auth-service/auth-api/cmd/seed` (permissions for resources including inventory; use the resource names above with the eight actions).
- Ordering-backend tenant sync: `ordering-service/ordering-backend/cmd/seed` and `internal/modules/identity/repository_ent.go` (`tenantSyncDestinations` includes `inventory-api`).
- inventory-ui: Sidebar and route visibility use the same permission codes (e.g. `items:read`, `warehouses:read`, `adjustments:read`); auth-api permission seeds should match so UI nav reflects correctly.
- Workspace rule: see `.cursor/rules/uniformity-rule.mdc` for RBAC and seed alignment across services.
