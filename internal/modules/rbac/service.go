package rbac

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/tenant"
)

// Service provides business logic for RBAC operations.
type Service struct {
	repo         Repository
	logger       *zap.Logger
	tenantSyncer *tenant.Syncer
}

// NewService creates a new RBAC service.
func NewService(repo Repository, logger *zap.Logger, tenantSyncer *tenant.Syncer) *Service {
	return &Service{
		repo:         repo,
		logger:       logger,
		tenantSyncer: tenantSyncer,
	}
}

// EnsureUserFromToken handles JIT provisioning of a user derived from an SSO token.
func (s *Service) EnsureUserFromToken(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, email string, tenantSlug string) (*InventoryUser, error) {
	// 1. If user already exists, we assume the tenant exists.
	user, err := s.repo.GetUserByAuthServiceID(ctx, tenantID, userID)
	if err == nil {
		return user, nil
	}

	// 2. User doesn't exist. Attempt to sync the tenant seamlessly first!
	if s.tenantSyncer != nil && tenantSlug != "" {
		_, syncErr := s.tenantSyncer.SyncTenant(ctx, tenantSlug)
		if syncErr != nil {
			s.logger.Warn("tenant sync failed during JIT user provisioning", zap.Error(syncErr))
		}
	}

	// 3. Create the user (reusing SyncUser logic)
	return s.SyncUser(ctx, tenantID, userID, email)
}

// SyncUser syncs a user from auth-service.
func (s *Service) SyncUser(ctx context.Context, tenantID uuid.UUID, authServiceUserID uuid.UUID, email string) (*InventoryUser, error) {
	// Check if user already exists
	user, err := s.repo.GetUserByAuthServiceID(ctx, tenantID, authServiceUserID)
	if err == nil {
		// User exists, update sync status
		updates := &UserUpdates{
			SyncStatus: stringPtr("synced"),
		}
		if err := s.repo.UpdateUser(ctx, tenantID, user.ID, updates); err != nil {
			s.logger.Warn("failed to update user sync status", zap.Error(err))
		}
		return user, nil
	}

	// Create new user
	user = &InventoryUser{
		ID:                uuid.New(),
		TenantID:          tenantID,
		AuthServiceUserID: authServiceUserID,
		Email:             email,
		Status:            "active",
		SyncStatus:        "synced",
	}

	if err := s.repo.CreateUser(ctx, tenantID, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	s.logger.Info("user synced",
		zap.String("tenant_id", tenantID.String()),
		zap.String("user_id", user.ID.String()),
		zap.String("email", email),
	)

	return user, nil
}

// HasPermission checks if a user has a specific permission.
func (s *Service) HasPermission(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, permissionCode string) (bool, error) {
	permissions, err := s.repo.GetUserPermissions(ctx, tenantID, userID)
	if err != nil {
		return false, fmt.Errorf("get user permissions: %w", err)
	}

	for _, perm := range permissions {
		if perm.PermissionCode == permissionCode {
			return true, nil
		}
	}

	return false, nil
}

// HasRole checks if a user has a specific role.
func (s *Service) HasRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleCode string) (bool, error) {
	roles, err := s.repo.GetUserRoles(ctx, tenantID, userID)
	if err != nil {
		return false, fmt.Errorf("get user roles: %w", err)
	}

	for _, role := range roles {
		if role.RoleCode == roleCode {
			return true, nil
		}
	}

	return false, nil
}

// AssignRole assigns a role to a user.
func (s *Service) AssignRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID, assignedBy uuid.UUID) error {
	// Check if assignment already exists
	assignments, err := s.repo.ListUserAssignments(ctx, tenantID, AssignmentFilters{
		UserID: &userID,
		RoleID: &roleID,
	})
	if err != nil {
		return fmt.Errorf("check existing assignment: %w", err)
	}

	if len(assignments) > 0 {
		return fmt.Errorf("role already assigned to user")
	}

	assignment := &UserRoleAssignment{
		ID:         uuid.New(),
		TenantID:   tenantID,
		UserID:     userID,
		RoleID:     roleID,
		AssignedBy: assignedBy,
	}

	if err := s.repo.AssignRoleToUser(ctx, tenantID, assignment); err != nil {
		return fmt.Errorf("assign role: %w", err)
	}

	s.logger.Info("role assigned",
		zap.String("tenant_id", tenantID.String()),
		zap.String("user_id", userID.String()),
		zap.String("role_id", roleID.String()),
		zap.String("assigned_by", assignedBy.String()),
	)

	return nil
}

// RevokeRole revokes a role from a user.
func (s *Service) RevokeRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error {
	if err := s.repo.RevokeRoleFromUser(ctx, tenantID, userID, roleID); err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}

	s.logger.Info("role revoked",
		zap.String("tenant_id", tenantID.String()),
		zap.String("user_id", userID.String()),
		zap.String("role_id", roleID.String()),
	)

	return nil
}

// GetUserRoles retrieves all roles for a user.
func (s *Service) GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryRole, error) {
	return s.repo.GetUserRoles(ctx, tenantID, userID)
}

// GetUserPermissions retrieves all permissions for a user.
func (s *Service) GetUserPermissions(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*InventoryPermission, error) {
	return s.repo.GetUserPermissions(ctx, tenantID, userID)
}

// Helper function
func stringPtr(s string) *string {
	return &s
}
