package rbac

import (
	"context"

	"github.com/google/uuid"
)

// Repository abstracts persistence for RBAC entities.
type Repository interface {
	// User operations
	CreateUser(ctx context.Context, tenantID uuid.UUID, user *InventoryUser) error
	GetUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) (*InventoryUser, error)
	GetUserByAuthServiceID(ctx context.Context, tenantID uuid.UUID, authServiceUserID uuid.UUID) (*InventoryUser, error)
	UpdateUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, updates *UserUpdates) error

	// Role operations
	CreateRole(ctx context.Context, tenantID uuid.UUID, role *InventoryRole) error
	GetRole(ctx context.Context, tenantID uuid.UUID, roleID uuid.UUID) (*InventoryRole, error)
	GetRoleByCode(ctx context.Context, tenantID uuid.UUID, roleCode string) (*InventoryRole, error)
	ListRoles(ctx context.Context, tenantID uuid.UUID) ([]*InventoryRole, error)

	// Permission operations
	CreatePermission(ctx context.Context, permission *InventoryPermission) error
	GetPermission(ctx context.Context, permissionID uuid.UUID) (*InventoryPermission, error)
	GetPermissionByCode(ctx context.Context, permissionCode string) (*InventoryPermission, error)
	ListPermissions(ctx context.Context, filters PermissionFilters) ([]*InventoryPermission, error)

	// Role-Permission operations
	AssignPermissionToRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error
	RemovePermissionFromRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error
	GetRolePermissions(ctx context.Context, roleID uuid.UUID) ([]*InventoryPermission, error)

	// User-Role assignment operations
	AssignRoleToUser(ctx context.Context, tenantID uuid.UUID, assignment *UserRoleAssignment) error
	RevokeRoleFromUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error
	GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryRole, error)
	GetUserPermissions(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryPermission, error)
	ListUserAssignments(ctx context.Context, tenantID uuid.UUID, filters AssignmentFilters) ([]*UserRoleAssignment, error)
}

// UserUpdates for partial user updates.
type UserUpdates struct {
	Status     *string
	SyncStatus *string
}

// PermissionFilters for listing permissions.
type PermissionFilters struct {
	Module *string
	Action *string
}

// AssignmentFilters for listing role assignments.
type AssignmentFilters struct {
	UserID *uuid.UUID
	RoleID *uuid.UUID
}
