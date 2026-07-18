package usersync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Service provisions users in auth-service (SSO) for a tenant. It calls auth-api's
// S2S member endpoint POST /api/v1/s2s/tenants/{tenant_id}/members (INTERNAL_SERVICE_KEY
// gated), which resolves-or-creates the account by email, upserts the tenant membership
// and publishes auth.user.created. (The previously-used shared auth-client SyncUser helper
// posted to /api/v1/admin/users/sync, a route that never existed on auth-api — every sync
// silently 404'd.)
type Service struct {
	baseURL string
	apiKey  string
	httpc   *http.Client
	logger  *zap.Logger
}

// NewService creates a new user sync service. apiKey must be the shared
// INTERNAL_SERVICE_KEY (sent as X-API-Key).
func NewService(authServiceURL, apiKey string, logger *zap.Logger) *Service {
	return &Service{
		baseURL: strings.TrimRight(authServiceURL, "/"),
		apiKey:  apiKey,
		httpc:   &http.Client{Timeout: 10 * time.Second},
		logger:  logger.Named("usersync"),
	}
}

// SyncUserRequest represents the request to provision a user with auth-service.
type SyncUserRequest struct {
	Email    string
	TenantID uuid.UUID
	// Roles for the tenant membership. Defaults to ["staff"]. NOTE: auth-api REPLACES
	// an existing membership's roles with this list — pass the user's real roles when
	// syncing someone who may already be a member.
	Roles   []string
	Service string
}

// SyncUserResponse represents the provisioning result.
type SyncUserResponse struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	TenantID string `json:"tenant_id"`
	Created  bool   `json:"created"`
}

// memberResponse mirrors auth-api's tenantMemberResponse (subset we need).
type memberResponse struct {
	UserID       string `json:"user_id"`
	TenantID     string `json:"tenant_id"`
	TempPassword string `json:"temp_password"`
}

// SyncUser provisions the user in auth-service and returns the auth user id.
func (s *Service) SyncUser(ctx context.Context, req SyncUserRequest) (*SyncUserResponse, error) {
	if s.apiKey == "" {
		s.logger.Warn("INTERNAL_SERVICE_KEY not configured, skipping user sync")
		return nil, fmt.Errorf("internal service key not configured")
	}
	if req.TenantID == uuid.Nil {
		return nil, fmt.Errorf("tenant id is required")
	}

	roles := req.Roles
	if len(roles) == 0 {
		roles = []string{"staff"}
	}
	service := req.Service
	if service == "" {
		service = "inventory"
	}

	body, err := json.Marshal(map[string]any{
		"email":   req.Email,
		"roles":   roles,
		"service": service,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal sync request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/s2s/tenants/%s/members", s.baseURL, req.TenantID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build sync request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", s.apiKey)

	resp, err := s.httpc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sync user request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		s.logger.Warn("user sync failed",
			zap.Int("status", resp.StatusCode),
			zap.Any("error", errResp),
			zap.String("email", req.Email),
		)
		return nil, fmt.Errorf("user sync failed: status %d", resp.StatusCode)
	}

	var member memberResponse
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		return nil, fmt.Errorf("decode sync response: %w", err)
	}
	tenantID := member.TenantID
	if tenantID == "" {
		tenantID = req.TenantID.String()
	}

	syncResp := &SyncUserResponse{
		UserID:   member.UserID,
		Email:    req.Email,
		TenantID: tenantID,
		// temp_password is only returned when a brand-new account was created.
		Created: member.TempPassword != "",
	}

	s.logger.Info("user synced with auth-service",
		zap.String("user_id", syncResp.UserID),
		zap.String("email", syncResp.Email),
		zap.Bool("created", syncResp.Created),
	)

	return syncResp, nil
}
