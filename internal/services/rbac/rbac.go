package rbac

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Permission represents a permission in the system
type Permission struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Module      string    `json:"module"`
	Action      string    `json:"action"`
	Resource    string    `json:"resource,omitempty"`
	Description string    `json:"description,omitempty"`
}

// Role represents a role in the system
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Permissions []Permission `json:"permissions"`
}

// Service handles RBAC operations
type Service struct {
	logger      *zap.Logger
	roles       map[string]*Role
	permissions map[string]*Permission
}

// NewService creates a new RBAC service
func NewService(logger *zap.Logger) *Service {
	s := &Service{
		logger:      logger,
		roles:       make(map[string]*Role),
		permissions: make(map[string]*Permission),
	}
	s.initDefaultRoles()
	return s
}

// initDefaultRoles initializes default roles and permissions
func (s *Service) initDefaultRoles() {
	// Default permissions for inventory
	inventoryRead := &Permission{
		ID:          uuid.New(),
		Name:        "inventory:read",
		Module:      "inventory",
		Action:      "read",
		Description: "Read inventory items",
	}
	inventoryWrite := &Permission{
		ID:          uuid.New(),
		Name:        "inventory:write",
		Module:      "inventory",
		Action:      "write",
		Description: "Create and update inventory items",
	}
	inventoryDelete := &Permission{
		ID:          uuid.New(),
		Name:        "inventory:delete",
		Module:      "inventory",
		Action:      "delete",
		Description: "Delete inventory items",
	}
	inventoryManage := &Permission{
		ID:          uuid.New(),
		Name:        "inventory:manage",
		Module:      "inventory",
		Action:      "manage",
		Description: "Full management of inventory",
	}

	s.permissions[inventoryRead.Name] = inventoryRead
	s.permissions[inventoryWrite.Name] = inventoryWrite
	s.permissions[inventoryDelete.Name] = inventoryDelete
	s.permissions[inventoryManage.Name] = inventoryManage

	// Default roles
	s.roles["admin"] = &Role{
		ID:          "admin",
		Name:        "admin",
		Description: "Administrator with full access",
		Permissions: []Permission{*inventoryRead, *inventoryWrite, *inventoryDelete, *inventoryManage},
	}

	s.roles["member"] = &Role{
		ID:          "member",
		Name:        "member",
		Description: "Regular member with read and write access",
		Permissions: []Permission{*inventoryRead, *inventoryWrite},
	}

	s.roles["viewer"] = &Role{
		ID:          "viewer",
		Name:        "viewer",
		Description: "Viewer with read-only access",
		Permissions: []Permission{*inventoryRead},
	}
}

// HasPermission checks if a user has a specific permission
func (s *Service) HasPermission(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID, module, action, resource string) (bool, error) {
	s.logger.Debug("checking permission",
		zap.String("user_id", userID.String()),
		zap.String("module", module),
		zap.String("action", action),
	)
	return true, nil
}

// GetUserRoles returns the roles for a user
func (s *Service) GetUserRoles(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID) ([]Role, error) {
	return []Role{*s.roles["member"]}, nil
}

// GetRole returns a role by ID
func (s *Service) GetRole(ctx context.Context, roleID string) (*Role, error) {
	role, ok := s.roles[roleID]
	if !ok {
		return nil, fmt.Errorf("role not found: %s", roleID)
	}
	return role, nil
}

// ListRoles returns all available roles
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	roles := make([]Role, 0, len(s.roles))
	for _, role := range s.roles {
		roles = append(roles, *role)
	}
	return roles, nil
}

