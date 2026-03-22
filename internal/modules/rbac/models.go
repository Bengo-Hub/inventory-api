package rbac

import (
	"time"

	"github.com/google/uuid"
)

// InventoryUser represents an inventory service user reference.
type InventoryUser struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	AuthServiceUserID uuid.UUID
	Email             string
	Status            string
	SyncStatus        string
	LastSyncAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// InventoryRole represents an inventory service role.
type InventoryRole struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	RoleCode     string
	Name         string
	Description  *string
	IsSystemRole bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// InventoryPermission represents an inventory service permission.
type InventoryPermission struct {
	ID             uuid.UUID
	PermissionCode string
	Name           string
	Module         string
	Action         string
	Resource       *string
	Description    *string
	CreatedAt      time.Time
}

// UserRoleAssignment represents a user role assignment.
type UserRoleAssignment struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	RoleID     uuid.UUID
	AssignedBy uuid.UUID
	AssignedAt time.Time
	ExpiresAt  *time.Time
}
