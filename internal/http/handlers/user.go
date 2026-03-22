package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/services/usersync"
)

// UserHandler handles user management operations
type UserHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
	syncService *usersync.Service
}

// NewUserHandler creates a new user handler
func NewUserHandler(logger *zap.Logger, rbacService *rbac.Service, syncService *usersync.Service) *UserHandler {
	return &UserHandler{
		logger:      logger,
		rbacService: rbacService,
		syncService: syncService,
	}
}

// CreateUserRequest represents a request to create a user
type CreateUserRequest struct {
	Email      string                 `json:"email"`
	Password   string                 `json:"password,omitempty"`
	TenantSlug string                 `json:"tenant_slug"`
	Profile    map[string]interface{} `json:"profile,omitempty"`
	Roles      []string               `json:"roles,omitempty"`
}

// CreateUser creates a new user and syncs with auth-service
func (h *UserHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	tenantID, _ := claims.TenantUUID()
	if tenantID == nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant ID required"})
		return
	}

	syncReq := usersync.SyncUserRequest{
		Email:      req.Email,
		Password:   req.Password,
		TenantSlug: req.TenantSlug,
		Profile:    req.Profile,
		Service:    "inventory-service",
	}

	syncResp, err := h.syncService.SyncUser(r.Context(), syncReq)
	if err != nil {
		h.logger.Error("failed to sync user with auth-service", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to sync user"})
		return
	}

	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"user_id":   syncResp.UserID,
		"email":     syncResp.Email,
		"tenant_id": syncResp.TenantID,
		"created":   true,
	})
}

// RegisterRoutes registers user management routes
func (h *UserHandler) RegisterRoutes(r chi.Router) {
	r.Post("/users", h.CreateUser)
}
