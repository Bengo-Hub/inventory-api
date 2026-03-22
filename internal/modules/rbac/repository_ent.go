package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorypermission"
	"github.com/bengobox/inventory-service/internal/ent/inventoryrole"
	"github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	"github.com/bengobox/inventory-service/internal/ent/rolepermission"
	"github.com/bengobox/inventory-service/internal/ent/userroleassignment"
	"github.com/google/uuid"
)

// EntRepository implements the Repository interface using Ent ORM.
type EntRepository struct {
	client *ent.Client
}

// NewEntRepository creates a new Ent-backed repository.
func NewEntRepository(client *ent.Client) *EntRepository {
	return &EntRepository{client: client}
}

// CreateUser persists a new inventory user reference.
func (r *EntRepository) CreateUser(ctx context.Context, tenantID uuid.UUID, user *InventoryUser) error {
	if user == nil {
		return errors.New("user cannot be nil")
	}

	builder := r.client.InventoryUser.Create().
		SetID(user.ID).
		SetTenantID(tenantID).
		SetAuthServiceUserID(user.AuthServiceUserID).
		SetEmail(user.Email).
		SetStatus(user.Status).
		SetSyncStatus(user.SyncStatus)

	if user.LastSyncAt != nil {
		builder.SetLastSyncAt(*user.LastSyncAt)
	}

	_, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	return nil
}

// GetUser retrieves a user by ID.
func (r *EntRepository) GetUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) (*InventoryUser, error) {
	entUser, err := r.client.InventoryUser.Query().
		Where(
			inventoryuser.ID(userID),
			inventoryuser.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("user not found: %w", err)
		}
		return nil, fmt.Errorf("get user: %w", err)
	}

	return mapEntUser(entUser), nil
}

// GetUserByAuthServiceID retrieves a user by auth-service user ID.
func (r *EntRepository) GetUserByAuthServiceID(ctx context.Context, tenantID uuid.UUID, authServiceUserID uuid.UUID) (*InventoryUser, error) {
	entUser, err := r.client.InventoryUser.Query().
		Where(
			inventoryuser.AuthServiceUserID(authServiceUserID),
			inventoryuser.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("user not found: %w", err)
		}
		return nil, fmt.Errorf("get user by auth service ID: %w", err)
	}

	return mapEntUser(entUser), nil
}

// UpdateUser updates a user.
func (r *EntRepository) UpdateUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, updates *UserUpdates) error {
	builder := r.client.InventoryUser.Update().
		Where(
			inventoryuser.ID(userID),
			inventoryuser.TenantID(tenantID),
		)

	if updates.Status != nil {
		builder.SetStatus(*updates.Status)
	}
	if updates.SyncStatus != nil {
		builder.SetSyncStatus(*updates.SyncStatus)
	}

	_, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}

	return nil
}

// CreateRole persists a new role.
func (r *EntRepository) CreateRole(ctx context.Context, tenantID uuid.UUID, role *InventoryRole) error {
	if role == nil {
		return errors.New("role cannot be nil")
	}

	builder := r.client.InventoryRole.Create().
		SetID(role.ID).
		SetTenantID(tenantID).
		SetRoleCode(role.RoleCode).
		SetName(role.Name).
		SetIsSystemRole(role.IsSystemRole)

	if role.Description != nil {
		builder.SetDescription(*role.Description)
	}

	_, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}

	return nil
}

// GetRole retrieves a role by ID.
func (r *EntRepository) GetRole(ctx context.Context, tenantID uuid.UUID, roleID uuid.UUID) (*InventoryRole, error) {
	entRole, err := r.client.InventoryRole.Query().
		Where(
			inventoryrole.ID(roleID),
			inventoryrole.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("role not found: %w", err)
		}
		return nil, fmt.Errorf("get role: %w", err)
	}

	return mapEntRole(entRole), nil
}

// GetRoleByCode retrieves a role by code.
func (r *EntRepository) GetRoleByCode(ctx context.Context, tenantID uuid.UUID, roleCode string) (*InventoryRole, error) {
	entRole, err := r.client.InventoryRole.Query().
		Where(
			inventoryrole.RoleCode(roleCode),
			inventoryrole.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("role not found: %w", err)
		}
		return nil, fmt.Errorf("get role by code: %w", err)
	}

	return mapEntRole(entRole), nil
}

// ListRoles lists all roles for a tenant.
func (r *EntRepository) ListRoles(ctx context.Context, tenantID uuid.UUID) ([]*InventoryRole, error) {
	entRoles, err := r.client.InventoryRole.Query().
		Where(inventoryrole.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}

	roles := make([]*InventoryRole, len(entRoles))
	for i, entRole := range entRoles {
		roles[i] = mapEntRole(entRole)
	}

	return roles, nil
}

// CreatePermission persists a new permission.
func (r *EntRepository) CreatePermission(ctx context.Context, permission *InventoryPermission) error {
	if permission == nil {
		return errors.New("permission cannot be nil")
	}

	builder := r.client.InventoryPermission.Create().
		SetID(permission.ID).
		SetPermissionCode(permission.PermissionCode).
		SetName(permission.Name).
		SetModule(permission.Module).
		SetAction(permission.Action)

	if permission.Resource != nil {
		builder.SetResource(*permission.Resource)
	}
	if permission.Description != nil {
		builder.SetDescription(*permission.Description)
	}

	_, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("create permission: %w", err)
	}

	return nil
}

// GetPermission retrieves a permission by ID.
func (r *EntRepository) GetPermission(ctx context.Context, permissionID uuid.UUID) (*InventoryPermission, error) {
	entPerm, err := r.client.InventoryPermission.Get(ctx, permissionID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("permission not found: %w", err)
		}
		return nil, fmt.Errorf("get permission: %w", err)
	}

	return mapEntPermission(entPerm), nil
}

// GetPermissionByCode retrieves a permission by code.
func (r *EntRepository) GetPermissionByCode(ctx context.Context, permissionCode string) (*InventoryPermission, error) {
	entPerm, err := r.client.InventoryPermission.Query().
		Where(inventorypermission.PermissionCode(permissionCode)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("permission not found: %w", err)
		}
		return nil, fmt.Errorf("get permission by code: %w", err)
	}

	return mapEntPermission(entPerm), nil
}

// ListPermissions lists permissions with optional filters.
func (r *EntRepository) ListPermissions(ctx context.Context, filters PermissionFilters) ([]*InventoryPermission, error) {
	query := r.client.InventoryPermission.Query()

	if filters.Module != nil {
		query = query.Where(inventorypermission.Module(*filters.Module))
	}
	if filters.Action != nil {
		query = query.Where(inventorypermission.Action(*filters.Action))
	}

	entPerms, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}

	permissions := make([]*InventoryPermission, len(entPerms))
	for i, entPerm := range entPerms {
		permissions[i] = mapEntPermission(entPerm)
	}

	return permissions, nil
}

// AssignPermissionToRole assigns a permission to a role.
func (r *EntRepository) AssignPermissionToRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error {
	_, err := r.client.RolePermission.Create().
		SetRoleID(roleID).
		SetPermissionID(permissionID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("assign permission to role: %w", err)
	}

	return nil
}

// RemovePermissionFromRole removes a permission from a role.
func (r *EntRepository) RemovePermissionFromRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error {
	_, err := r.client.RolePermission.Delete().
		Where(
			rolepermission.RoleID(roleID),
			rolepermission.PermissionID(permissionID),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("remove permission from role: %w", err)
	}

	return nil
}

// GetRolePermissions retrieves all permissions for a role.
func (r *EntRepository) GetRolePermissions(ctx context.Context, roleID uuid.UUID) ([]*InventoryPermission, error) {
	entPerms, err := r.client.InventoryRole.Query().
		Where(inventoryrole.ID(roleID)).
		QueryPermissions().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get role permissions: %w", err)
	}

	permissions := make([]*InventoryPermission, len(entPerms))
	for i, entPerm := range entPerms {
		permissions[i] = mapEntPermission(entPerm)
	}

	return permissions, nil
}

// AssignRoleToUser assigns a role to a user.
func (r *EntRepository) AssignRoleToUser(ctx context.Context, tenantID uuid.UUID, assignment *UserRoleAssignment) error {
	if assignment == nil {
		return errors.New("assignment cannot be nil")
	}

	builder := r.client.UserRoleAssignment.Create().
		SetID(assignment.ID).
		SetTenantID(tenantID).
		SetUserID(assignment.UserID).
		SetRoleID(assignment.RoleID).
		SetAssignedBy(assignment.AssignedBy)

	if assignment.ExpiresAt != nil {
		builder.SetExpiresAt(*assignment.ExpiresAt)
	}

	_, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("assign role to user: %w", err)
	}

	return nil
}

// RevokeRoleFromUser revokes a role from a user.
func (r *EntRepository) RevokeRoleFromUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error {
	_, err := r.client.UserRoleAssignment.Delete().
		Where(
			userroleassignment.TenantID(tenantID),
			userroleassignment.UserID(userID),
			userroleassignment.RoleID(roleID),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke role from user: %w", err)
	}

	return nil
}

// GetUserRoles retrieves all roles assigned to a user.
func (r *EntRepository) GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryRole, error) {
	entRoles, err := r.client.UserRoleAssignment.Query().
		Where(
			userroleassignment.TenantID(tenantID),
			userroleassignment.UserID(userID),
		).
		QueryRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user roles: %w", err)
	}

	roles := make([]*InventoryRole, len(entRoles))
	for i, entRole := range entRoles {
		roles[i] = mapEntRole(entRole)
	}

	return roles, nil
}

// GetUserPermissions retrieves all permissions for a user (via their roles).
func (r *EntRepository) GetUserPermissions(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryPermission, error) {
	// Get user's roles first
	roles, err := r.GetUserRoles(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}

	// Collect all unique permissions from all roles
	permissionMap := make(map[uuid.UUID]*InventoryPermission)
	for _, role := range roles {
		rolePerms, err := r.GetRolePermissions(ctx, role.ID)
		if err != nil {
			continue
		}
		for _, perm := range rolePerms {
			permissionMap[perm.ID] = perm
		}
	}

	permissions := make([]*InventoryPermission, 0, len(permissionMap))
	for _, perm := range permissionMap {
		permissions = append(permissions, perm)
	}

	return permissions, nil
}

// ListUserAssignments lists role assignments with optional filters.
func (r *EntRepository) ListUserAssignments(ctx context.Context, tenantID uuid.UUID, filters AssignmentFilters) ([]*UserRoleAssignment, error) {
	query := r.client.UserRoleAssignment.Query().
		Where(userroleassignment.TenantID(tenantID))

	if filters.UserID != nil {
		query = query.Where(userroleassignment.UserID(*filters.UserID))
	}
	if filters.RoleID != nil {
		query = query.Where(userroleassignment.RoleID(*filters.RoleID))
	}

	entAssignments, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user assignments: %w", err)
	}

	assignments := make([]*UserRoleAssignment, len(entAssignments))
	for i, entAssignment := range entAssignments {
		assignments[i] = mapEntAssignment(entAssignment)
	}

	return assignments, nil
}

// Mapping functions

func mapEntUser(entUser *ent.InventoryUser) *InventoryUser {
	user := &InventoryUser{
		ID:                entUser.ID,
		TenantID:          entUser.TenantID,
		AuthServiceUserID: entUser.AuthServiceUserID,
		Email:             entUser.Email,
		Status:            entUser.Status,
		SyncStatus:        entUser.SyncStatus,
		CreatedAt:         entUser.CreatedAt,
		UpdatedAt:         entUser.UpdatedAt,
	}

	if !entUser.LastSyncAt.IsZero() {
		user.LastSyncAt = &entUser.LastSyncAt
	}

	return user
}

func mapEntRole(entRole *ent.InventoryRole) *InventoryRole {
	role := &InventoryRole{
		ID:           entRole.ID,
		TenantID:     entRole.TenantID,
		RoleCode:     entRole.RoleCode,
		Name:         entRole.Name,
		IsSystemRole: entRole.IsSystemRole,
		CreatedAt:    entRole.CreatedAt,
		UpdatedAt:    entRole.UpdatedAt,
	}

	if entRole.Description != "" {
		role.Description = &entRole.Description
	}

	return role
}

func mapEntPermission(entPerm *ent.InventoryPermission) *InventoryPermission {
	perm := &InventoryPermission{
		ID:             entPerm.ID,
		PermissionCode: entPerm.PermissionCode,
		Name:           entPerm.Name,
		Module:         entPerm.Module,
		Action:         entPerm.Action,
		CreatedAt:      entPerm.CreatedAt,
	}

	if entPerm.Resource != "" {
		perm.Resource = &entPerm.Resource
	}
	if entPerm.Description != "" {
		perm.Description = &entPerm.Description
	}

	return perm
}

func mapEntAssignment(entAssignment *ent.UserRoleAssignment) *UserRoleAssignment {
	assignment := &UserRoleAssignment{
		ID:         entAssignment.ID,
		TenantID:   entAssignment.TenantID,
		UserID:     entAssignment.UserID,
		RoleID:     entAssignment.RoleID,
		AssignedBy: entAssignment.AssignedBy,
		AssignedAt: entAssignment.AssignedAt,
	}

	if !entAssignment.ExpiresAt.IsZero() {
		assignment.ExpiresAt = &entAssignment.ExpiresAt
	}

	return assignment
}
