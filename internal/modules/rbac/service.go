package rbac

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/tenant"
)

// Service provides business logic for RBAC operations.
type Service struct {
	repo         Repository
	logger       *zap.Logger
	tenantSyncer *tenant.Syncer

	// strictRBAC, when true (the default), makes the permission middleware treat tenant
	// superusers/admins as ordinary RBAC subjects: they must hold an EXPLICIT inventory role
	// (granted via JIT provisioning, see assignDefaultRoleFromJWT) rather than silently
	// bypassing permission checks just for carrying the global superuser/admin role.
	// Only IsPlatformOwner (the platform tenant) bypasses unconditionally.
	//
	// Escape hatch: set INVENTORY_STRICT_RBAC=false to restore the legacy blanket
	// superuser/admin bypass if the role-grant migration ever fails to cover a live user.
	strictRBAC bool
}

// NewService creates a new RBAC service.
func NewService(repo Repository, logger *zap.Logger, tenantSyncer *tenant.Syncer) *Service {
	return &Service{
		repo:         repo,
		logger:       logger,
		tenantSyncer: tenantSyncer,
		strictRBAC:   strictRBACEnabled(),
	}
}

// strictRBACEnabled reads INVENTORY_STRICT_RBAC. It defaults to true (strict); only an
// explicit falsy value ("false", "0", "no", "off") disables strict mode.
func strictRBACEnabled() bool {
	v := os.Getenv("INVENTORY_STRICT_RBAC")
	if v == "" {
		return true
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	switch v {
	case "no", "No", "NO", "off", "Off", "OFF":
		return false
	}
	return true
}

// StrictRBAC reports whether strict inventory RBAC is enabled (the default). When false,
// the legacy superuser/admin blanket bypass is restored.
func (s *Service) StrictRBAC() bool {
	return s.strictRBAC
}

// EnsureUserFromToken handles JIT provisioning of a user derived from an SSO token.
// It also assigns service-level roles based on the user's global roles from JWT claims.
func (s *Service) EnsureUserFromToken(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, email string, tenantSlug string, roles ...string) (*InventoryUser, error) {
	// 1. If user already exists, check if they need a role assignment (e.g. roles were seeded after user was first provisioned).
	user, err := s.repo.GetUserByAuthServiceID(ctx, tenantID, userID)
	if err == nil {
		// Re-run role assignment idempotently in case the role was unavailable on first provisioning.
		s.assignDefaultRoleFromJWT(ctx, tenantID, user.ID, userID, roles)
		return user, nil
	}

	// 2. User doesn't exist. Attempt to sync the tenant seamlessly first.
	if s.tenantSyncer != nil && tenantSlug != "" {
		_, syncErr := s.tenantSyncer.SyncTenant(ctx, tenantSlug)
		if syncErr != nil {
			s.logger.Warn("tenant sync failed during JIT user provisioning", zap.Error(syncErr))
		}
	}

	// 3. Create the user (reusing SyncUser logic). The display name is backfilled later by the
	// auth.user event consumer (which carries full_name); not available from the token here.
	user, err = s.SyncUser(ctx, tenantID, userID, email, "")
	if err != nil {
		return nil, err
	}

	// 4. Assign default service-level role based on global JWT roles
	s.assignDefaultRoleFromJWT(ctx, tenantID, user.ID, userID, roles)

	return user, nil
}

// assignDefaultRoleFromJWT maps global JWT roles to inventory-api service-level roles.
// superuser/admin → inventory_admin, staff → warehouse_manager, others → viewer.
func (s *Service) assignDefaultRoleFromJWT(ctx context.Context, tenantID uuid.UUID, localUserID uuid.UUID, authUserID uuid.UUID, roles []string) {
	roleCode := mapGlobalRoleToInventoryRole(roles)
	if roleCode == "" {
		return
	}

	role, err := s.repo.GetRoleByCode(ctx, tenantID, roleCode)
	if err != nil {
		// The role hasn't been seeded for this tenant/deployment yet. Provision it on the fly so a
		// tenant admin (mapped to inventory_admin) is never locked out of permission-gated pages
		// just because the role-seed step never ran. Without this, GetRoleByCode failed silently
		// and the admin ended up with NO inventory role → 403 on every gated route.
		role, err = s.ensureRoleByCode(ctx, tenantID, roleCode)
		if err != nil {
			s.logger.Warn("JIT role provisioning failed",
				zap.String("role_code", roleCode),
				zap.String("tenant_id", tenantID.String()),
				zap.Error(err),
			)
			return
		}
	}

	// Idempotent: check if already assigned
	assignments, err := s.repo.ListUserAssignments(ctx, tenantID, AssignmentFilters{
		UserID: &localUserID,
		RoleID: &role.ID,
	})
	if err == nil && len(assignments) > 0 {
		return
	}

	assignment := &UserRoleAssignment{
		ID:         uuid.New(),
		TenantID:   tenantID,
		UserID:     localUserID,
		RoleID:     role.ID,
		AssignedBy: authUserID,
	}
	if err := s.repo.AssignRoleToUser(ctx, tenantID, assignment); err != nil {
		s.logger.Warn("JIT role assignment failed",
			zap.String("role_code", roleCode),
			zap.Error(err),
		)
		return
	}

	s.logger.Info("JIT assigned inventory role",
		zap.String("role_code", roleCode),
		zap.String("user_id", localUserID.String()),
		zap.String("tenant_id", tenantID.String()),
	)
}

// ensureRoleByCode returns the role for a code, creating a tenant-scoped role when
// neither a global system role nor a tenant override exists yet. This keeps JIT role
// assignment self-healing so a tenant admin always ends up holding inventory_admin
// (which the permission middleware treats as a full bypass) even on a deployment where
// the role catalog was never seeded. Idempotent under the unique (tenant, role_code).
func (s *Service) ensureRoleByCode(ctx context.Context, tenantID uuid.UUID, roleCode string) (*InventoryRole, error) {
	if role, err := s.repo.GetRoleByCode(ctx, tenantID, roleCode); err == nil {
		return role, nil
	}
	role := &InventoryRole{
		ID:           uuid.New(),
		TenantID:     &tenantID,
		RoleCode:     roleCode,
		Name:         humanizeRoleCode(roleCode),
		IsSystemRole: true,
	}
	if err := s.repo.CreateRole(ctx, tenantID, role); err != nil {
		// Lost a race, or it already exists — re-fetch rather than fail.
		if existing, gerr := s.repo.GetRoleByCode(ctx, tenantID, roleCode); gerr == nil {
			return existing, nil
		}
		return nil, err
	}
	s.logger.Info("provisioned inventory role on demand",
		zap.String("role_code", roleCode), zap.String("tenant_id", tenantID.String()))
	return role, nil
}

// humanizeRoleCode turns a snake_case role code into a display name ("inventory_admin" → "Inventory Admin").
func humanizeRoleCode(code string) string {
	parts := strings.Split(code, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// IsAdminRoles reports whether any of the given global SSO roles grants tenant-admin
// (full inventory) access — i.e. maps to inventory_admin.
func IsAdminRoles(roles []string) bool {
	return mapGlobalRoleToInventoryRole(roles) == RoleInventoryAdmin
}

// mapGlobalRoleToInventoryRole maps global SSO roles to inventory service roles.
// Priority order: most privileged role wins. Matching is case-insensitive and covers
// the common tenant-admin aliases so a tenant owner/admin always resolves to
// inventory_admin (full access) regardless of how auth named the role.
func mapGlobalRoleToInventoryRole(roles []string) string {
	best := ""
	rank := func(code string) int {
		switch code {
		case "inventory_admin":
			return 4
		case "warehouse_manager":
			return 3
		case "accountant":
			// Peer of warehouse_manager: distinct permission set (owns procurement/assets/approvals)
			// but a comparable privilege tier, so it outranks stock_clerk/viewer.
			return 3
		case "stock_clerk":
			return 2
		case "viewer":
			return 1
		}
		return 0
	}
	for _, r := range roles {
		var mapped string
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "superuser", "admin", "administrator", "tenant_admin", "tenantadmin",
			"tenant-admin", "owner", "account_owner", "org_admin", "orgadmin",
			"organization_admin", "proprietor", "director":
			mapped = "inventory_admin"
		case "staff", "manager", "store_manager", "storemanager", "supervisor", "branch_manager":
			mapped = "warehouse_manager"
		case "accountant", "finance", "finance_manager":
			mapped = "accountant"
		case "cashier", "kitchen", "bar", "barista", "stock_clerk", "storekeeper", "waiter":
			mapped = "stock_clerk"
		default:
			mapped = "viewer"
		}
		if rank(mapped) > rank(best) {
			best = mapped
		}
	}
	if best == "" {
		return "viewer"
	}
	return best
}

// SyncUser syncs a user from auth-service.
func (s *Service) SyncUser(ctx context.Context, tenantID uuid.UUID, authServiceUserID uuid.UUID, email, name string) (*InventoryUser, error) {
	// Check if user already exists
	user, err := s.repo.GetUserByAuthServiceID(ctx, tenantID, authServiceUserID)
	if err == nil {
		// User exists, update sync status (and refresh the display name when supplied).
		updates := &UserUpdates{
			SyncStatus: stringPtr("synced"),
		}
		if name != "" {
			updates.Name = &name
			user.Name = name
		}
		if err := s.repo.UpdateUser(ctx, tenantID, user.ID, updates); err != nil {
			s.logger.Warn("failed to update user sync status", zap.Error(err))
		}
		return user, nil
	}

	// Create new user — use auth-service UUID as PK for cross-service consistency
	user = &InventoryUser{
		ID:                authServiceUserID,
		TenantID:          tenantID,
		AuthServiceUserID: authServiceUserID,
		Email:             email,
		Name:              name,
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
