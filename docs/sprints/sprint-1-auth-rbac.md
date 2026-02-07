# Sprint 1 – Authentication, RBAC & User Management

**Status**: ⏳ Not Started  
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
- [ ] JWT validation middleware configured via `shared-auth-client` (✅ Already done)
- [ ] All `/api/v1/{tenantID}` routes protected
- [ ] Tenant ID extracted from JWT claims
- [ ] User ID extracted from JWT claims

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
- [ ] Ent schema for `inventory_roles` table
- [ ] Ent schema for `inventory_permissions` table
- [ ] Ent schema for `role_permissions` junction table
- [ ] Ent schema for `user_role_assignments` table
- [ ] Seed data for 3 default roles (Warehouse Manager, Stock Keeper, Viewer)
- [ ] Seed data for all inventory permissions
- [ ] Role-permission mappings defined

### US-1.4: Permission Middleware
**As a** system  
**I want** permission checking middleware  
**So that** endpoints enforce RBAC

**Acceptance Criteria**:
- [ ] `RequirePermission(permission string)` middleware
- [ ] `RequireRole(role string)` middleware
- [ ] Permission check against user's assigned roles
- [ ] Forbidden (403) response for unauthorized access
- [ ] Superuser bypass (from JWT claims)

### US-1.5: Role Assignment API
**As a** warehouse administrator  
**I want** to assign roles to users  
**So that** users have appropriate permissions

**Acceptance Criteria**:
- [ ] `POST /api/v1/{tenantID}/rbac/assignments` - Assign role
- [ ] `GET /api/v1/{tenantID}/rbac/assignments` - List assignments
- [ ] `DELETE /api/v1/{tenantID}/rbac/assignments/{id}` - Revoke role
- [ ] Only Warehouse Manager can assign roles

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

- [ ] Create Ent schemas for RBAC (inventory_users, inventory_roles, inventory_permissions, role_permissions, user_role_assignments)
- [ ] Implement user sync service (similar to logistics-service)
- [ ] Create RBAC service layer
- [ ] Create RBAC repository layer
- [ ] Implement permission middleware
- [ ] Create role assignment handlers
- [ ] Create user management handlers
- [ ] Seed default roles and permissions
- [ ] Wire RBAC middleware to router
- [ ] Add event listeners for auth.user.* events

---

## Next Sprint

- Sprint 2: Master Data (can only proceed after auth/RBAC is complete)

