# Sprint 1 – Authentication, RBAC & User Management

**Status**: ✅ Complete — JWT/RBAC middleware, schemas, permission checks, role assignment API, event listeners, ListUsers/GetUser handlers all done; `GET /auth/me` implemented and registered; 4 roles + 99 permissions seeded in `cmd/seed/main.go`; explicit user sync service still pending (non-blocking)  
**Priority**: **CRITICAL - MUST BE FIRST SPRINT**  
**Start Date**: TBD  
**Duration**: 2-3 weeks

## Overview

Sprint 1 focuses on implementing service-level authentication, RBAC, permissions, and user management integrated with auth-service SSO. This is the foundation that all other features depend on - endpoints cannot be authenticated without this.

---

## Goals

1. Integrate auth-service SSO (JWT validation via `shared-auth-client`)
2. Implement service-specific RBAC for inventory operations
3. Create user sync with auth-service
4. Define warehouse roles and permissions
5. Implement permission checking middleware
6. Create role assignment APIs

---

## Warehouse Roles & Permissions

### Roles

**1. Warehouse Manager**
- Full access to all inventory operations
- Can manage users, roles, configurations
- Can approve/reject inventory adjustments

**2. Stock Keeper**
- Can create/edit items, stock movements
- Can process stock adjustments
- Can create purchase orders
- Cannot approve high-value adjustments

**3. Inventory Viewer**
- Read-only access to inventory data
- Can view items, stock levels, reports
- Cannot modify any data

### Permissions

**Item Permissions:**
- `inventory.items.create` - Create items/SKUs
- `inventory.items.edit` - Edit items
- `inventory.items.delete` - Delete items
- `inventory.items.view` - View items

**Stock Permissions:**
- `inventory.stock.adjust` - Adjust stock levels
- `inventory.stock.move` - Move stock between locations
- `inventory.stock.view` - View stock levels
- `inventory.stock.approve` - Approve stock adjustments

**Purchase Order Permissions:**
- `inventory.purchase_orders.create` - Create purchase orders
- `inventory.purchase_orders.approve` - Approve purchase orders
- `inventory.purchase_orders.view` - View purchase orders

**Warehouse Permissions:**
- `inventory.warehouses.manage` - Manage warehouses
- `inventory.warehouses.view` - View warehouses

**Configuration Permissions:**
- `inventory.config.view` - View configuration
- `inventory.config.manage` - Manage configuration
- `inventory.users.manage` - Manage users and roles

---

## User Stories

### US-1.1: Auth-Service SSO Integration
**As a** system  
**I want** all requests validated via auth-service JWT tokens  
**So that** only authenticated users can access inventory endpoints

**Acceptance Criteria**:
- [x] JWT validation middleware configured via `shared-auth-client` (✅ Already done)
- [x] All `/api/v1/{tenantID}` routes protected via `authMiddleware.RequireAuth`
- [x] Tenant ID extracted from JWT claims via `httpware.TenantV2`
- [x] User ID extracted from JWT claims in `RequirePermission` middleware

### US-1.2: User Synchronization
**As a** system  
**I want** users synced from auth-service  
**So that** inventory service has user references for operations

**Acceptance Criteria**:
- [ ] User sync service implemented (similar to logistics-service)
- [ ] Local user reference table (`inventory_users`)
- [ ] User sync on login/first access
- [ ] Consume `auth.user.created`, `auth.user.updated` events
- [ ] `auth_service_user_id` stored for reference

### US-1.3: Inventory RBAC Implementation
**As a** warehouse administrator  
**I want** inventory-specific roles and permissions  
**So that** users have appropriate access to inventory operations

**Acceptance Criteria**:
- [x] Ent schema for `inventory_roles` table
- [x] Ent schema for `inventory_permissions` table
- [x] Ent schema for `role_permissions` junction table
- [x] Ent schema for `user_role_assignments` table
- [x] Seed data for 4 default roles (`inventory_admin`, `warehouse_manager`, `stock_clerk`, `viewer`) — `cmd/seed/main.go:seedRoles()`
- [x] Seed data for all inventory permissions (99 permissions across 11 modules) — `cmd/seed/main.go:seedPermissions()`
- [x] Role-permission mappings defined — `cmd/seed/main.go:seedRolePermissions()` with `rolePermMap`

### US-1.4: Permission Middleware
**As a** system  
**I want** permission checking middleware  
**So that** endpoints enforce RBAC

**Acceptance Criteria**:
- [x] `RequirePermission(permission string)` middleware (`internal/http/middleware/rbac.go`)
- [x] `RequireAnyPermission(permissions ...string)` middleware
- [x] Permission check against user's assigned roles via `rbac.Service.HasPermission()`
- [x] Forbidden (403) response for unauthorized access
- [x] Platform owner bypass + `inventory_admin` role bypass

### US-1.5: Role Assignment API
**As a** warehouse administrator  
**I want** to assign roles to users  
**So that** users have appropriate permissions

**Acceptance Criteria**:
- [x] `POST /api/v1/{tenantID}/rbac/assignments` - Assign role
- [x] `GET /api/v1/{tenantID}/rbac/assignments` - List assignments
- [x] `DELETE /api/v1/{tenantID}/rbac/assignments/{id}` - Revoke role
- [ ] Only Warehouse Manager can assign roles — permission middleware not scoped to `warehouse_manager` role specifically; `inventory.users.manage` permission enforced but any role holding it can assign

### US-1.6: GET /auth/me Endpoint
**As a** inventory-ui frontend  
**I want** a service-specific auth/me endpoint  
**So that** the UI can bootstrap local RBAC roles and permissions after SSO login

**Acceptance Criteria**:
- [x] `GET /api/v1/{tenantID}/auth/me` — implemented in `internal/http/handlers/auth.go`
- [x] Returns JWT claims augmented with service-level RBAC roles and permissions
- [x] Registered in router via `authHandler.RegisterAuthRoutes(private)`

---

## Database Schema

### inventory_users
- `id` (UUID, PK)
- `tenant_id` (UUID, FK → tenants)
- `auth_service_user_id` (UUID, UNIQUE) - Reference to auth-service
- `email` (VARCHAR) - Denormalized for convenience
- `status` (VARCHAR) - active, inactive, suspended
- `sync_status` (VARCHAR) - synced, pending, failed
- `last_sync_at` (TIMESTAMPTZ)
- `created_at`, `updated_at` (TIMESTAMPTZ)

### inventory_roles
- `id` (UUID, PK)
- `tenant_id` (UUID, FK → tenants)
- `role_code` (VARCHAR) - warehouse_manager, stock_keeper, viewer
- `name` (VARCHAR) - Display name
- `description` (TEXT)
- `is_system_role` (BOOLEAN) - System roles cannot be deleted
- `created_at`, `updated_at` (TIMESTAMPTZ)

### inventory_permissions
- `id` (UUID, PK)
- `permission_code` (VARCHAR, UNIQUE) - inventory.items.create, etc.
- `name` (VARCHAR)
- `module` (VARCHAR) - items, stock, purchase_orders, warehouses
- `action` (VARCHAR) - create, edit, approve, view, delete
- `resource` (VARCHAR) - items, stock, etc.
- `description` (TEXT)
- `created_at` (TIMESTAMPTZ)

### role_permissions
- `role_id` (UUID, FK → inventory_roles)
- `permission_id` (UUID, FK → inventory_permissions)
- Composite PK: (role_id, permission_id)

### user_role_assignments
- `id` (UUID, PK)
- `tenant_id` (UUID, FK → tenants)
- `user_id` (UUID, FK → inventory_users)
- `role_id` (UUID, FK → inventory_roles)
- `assigned_by` (UUID, FK → inventory_users)
- `assigned_at` (TIMESTAMPTZ)
- `expires_at` (TIMESTAMPTZ, Optional)
- Unique constraint: (tenant_id, user_id, role_id)

---

## Implementation Tasks

- [x] Create Ent schemas for RBAC (inventory_users, inventory_roles, inventory_permissions, role_permissions, user_role_assignments)
- [ ] Implement user sync service (JIT provisioning via auth events already wired; explicit sync service pending)
- [x] Create RBAC service layer (`rbac/service.go`)
- [x] Create RBAC repository layer (`rbac/repository_ent.go`)
- [x] Implement permission middleware (`http/middleware/rbac.go`)
- [x] Create role assignment handlers (`http/handlers/rbac.go`)
- [x] Create user management handlers (`http/handlers/user.go` — ListUsers/GetUser confirmed by handler file)
- [x] Seed default roles and permissions — `seedRoles()` + `seedRolePermissions()` + `seedPermissions()` in `cmd/seed/main.go`: 4 roles (`inventory_admin`, `warehouse_manager`, `stock_clerk`, `viewer`) + 99 permissions (11 modules × 9 actions)
- [x] Wire RBAC middleware to router (per-route with `perm()` helper)
- [x] Add event listeners for auth.user.* events (`consumers/auth_events.go`)

## Security Hardening (2026-05-21)
- [x] Hardcoded `Vertex2020!` credentials removed from `docs/database_maintenance.md` (replaced with K8s secret refs)
- [x] Request body size limited to 10MB (`middleware.RequestSize`) — prevents memory DoS
- [x] IP rate limiting via Redis sliding window — 100 req/60s per IP, returns 429 + `X-RateLimit-*` headers
- [x] `HTTP_ALLOWED_ORIGINS` default updated to production-only origins (localhost removed)
- [x] `POST /api/v1/media/upload` now requires authentication (was unauthenticated)

## Completion Notes (2026-05-21)

Sprint 1 RBAC backbone is complete. `user.go` handler file exists with `ListUsers`/`GetUser`. RBAC role assignment API endpoints (`POST /rbac/assignments`, `GET /rbac/assignments`, `DELETE /rbac/assignments/{id}`) registered via `rbacHandler.RegisterRBACRoutes(private)`. 4 roles seeded: `inventory_admin`, `warehouse_manager`, `stock_clerk`, `viewer` (99 permissions). Outlet-aware warehouse routing added (Sprint 2 ERP gaps). Seed guard for recipes added. `outlet_id` FK added to warehouse schema. Pricing tiers and warehouse locations schemas/handlers added (ERP gaps sprint).

---

## Next Sprint

- Sprint 2: Master Data (can only proceed after auth/RBAC is complete)

