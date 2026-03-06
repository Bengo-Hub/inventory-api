package usersync

import (
	"context"
	"fmt"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"go.uber.org/zap"
)

// Service handles user synchronization with auth-service SSO
type Service struct {
	client *authclient.Client
	apiKey string
	logger *zap.Logger
}

// NewService creates a new user sync service
func NewService(authServiceURL, apiKey string, logger *zap.Logger) *Service {
	return &Service{
		client: authclient.NewClient(authServiceURL, logger),
		apiKey: apiKey,
		logger: logger,
	}
}

// SyncUserRequest is an alias for shared client request to maintain compatibility if needed,
// or we can use the shared type directly in handlers. For now, we'll keep the handler using the local type
// (or update the handler) but map it here, OR just use the shared type.
// The handler defines `usersync.SyncUserRequest`? No, it uses `usersync.SyncUserRequest` struct defined in `sync.go`.
// Let's replace the local types with type aliases or just remove them and use shared types.

// SyncUserRequest represents the request to sync a user with auth-service
type SyncUserRequest = authclient.SyncUserRequest

// SyncUserResponse represents the response from auth-service
type SyncUserResponse = authclient.SyncUserResponse

// SyncUser syncs a user with auth-service SSO
func (s *Service) SyncUser(ctx context.Context, req SyncUserRequest) (*SyncUserResponse, error) {
	if s.apiKey == "" {
		s.logger.Warn("auth-service API key not configured, skipping user sync")
		return nil, fmt.Errorf("auth-service API key not configured")
	}

	return s.client.SyncUser(ctx, req, s.apiKey)
}
