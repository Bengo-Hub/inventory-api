package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// AuthHandler handles service-level auth sync for inventory-ui.
type AuthHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
	orm         *ent.Client
	authURL     string
	internalKey string
	http        *http.Client
}

// NewAuthHandler creates a new AuthHandler. authURL/internalKey let /auth/me forward the
// email-verification block from auth-api so the UI can show the graduated verify banner.
func NewAuthHandler(logger *zap.Logger, rbacService *rbac.Service, orm *ent.Client, authURL, internalKey string) *AuthHandler {
	return &AuthHandler{
		logger:      logger.Named("auth.Handler"),
		rbacService: rbacService,
		orm:         orm,
		authURL:     authURL,
		internalKey: internalKey,
		http:        &http.Client{Timeout: 5 * time.Second},
	}
}

// fetchEmailVerification returns auth-api's computed email-verification block for the user
// (opaque JSON, forwarded verbatim to the UI). Best-effort: returns nil on any error so
// /auth/me never fails because of it.
func (h *AuthHandler) fetchEmailVerification(ctx context.Context, userID uuid.UUID) json.RawMessage {
	if h.authURL == "" || h.internalKey == "" {
		return nil
	}
	url := strings.TrimRight(h.authURL, "/") + "/api/v1/s2s/users/" + userID.String() + "/email-verification"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-API-Key", h.internalKey)
	resp, err := h.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}
	return raw
}

// MeResponse is the payload returned by GET /auth/me.
type MeResponse struct {
	ID              string   `json:"id"`
	Email           string   `json:"email"`
	TenantID        string   `json:"tenant_id"`
	TenantSlug      string   `json:"tenant_slug"`
	Roles           []string `json:"roles"`
	Permissions     []string `json:"permissions"`
	IsPlatformOwner bool     `json:"is_platform_owner"`
	IsSuperUser     bool     `json:"is_superuser"`
	// Resolved home outlet for this user in this tenant. Auto-linked to the tenant default
	// when the user had none, so the UI never loads an empty outlet while the tenant has one.
	OutletID   string `json:"outlet_id,omitempty"`
	OutletName string `json:"outlet_name,omitempty"`
	// EmailVerification is auth-api's computed graduated verify state, forwarded verbatim so
	// the UI can render the same banner as the accounts portal.
	EmailVerification json.RawMessage `json:"email_verification,omitempty"`
}

// Me handles GET /auth/me.
// Called by inventory-ui after SSO callback to sync local RBAC roles and permissions.
// JWT is already validated by auth middleware; user is JIT-provisioned by the router middleware.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing claims")
		return
	}

	userID, err := claims.UserID()
	if err != nil || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid user ID in token")
		return
	}

	tenantID, err := claims.TenantUUID()
	if err != nil || tenantID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid tenant ID in token")
		return
	}

	// Persist the user's service-level inventory role from their claims on every login, so
	// local RBAC stays in sync for BOTH auth flows: SSO claims carry the global tenant roles,
	// and terminal/PIN claims carry the already-mapped inventory role codes (idempotent). This
	// is the belt-and-braces companion to the auth.user event consumer — a user who reaches the
	// API before their event is processed still gets provisioned. Skipped when claims carry no
	// roles so we never assign a spurious default.
	if h.rbacService != nil && len(claims.Roles) > 0 {
		if _, perr := h.rbacService.EnsureUserFromToken(r.Context(), *tenantID, userID, claims.Email, claims.GetTenantSlug(), claims.Roles...); perr != nil {
			h.logger.Debug("auth/me: service-role sync failed", zap.Error(perr))
		}
	}

	// Base roles/permissions come from JWT claims (always present immediately after SSO login).
	roles := make([]string, len(claims.Roles))
	copy(roles, claims.Roles)

	permissions := make([]string, len(claims.Permissions))
	copy(permissions, claims.Permissions)

	// Augment with service-level RBAC assignments stored in inventory DB.
	if h.rbacService != nil {
		if svcRoles, rErr := h.rbacService.GetUserRoles(r.Context(), *tenantID, userID); rErr == nil {
			for _, sr := range svcRoles {
				roles = appendUniqueStr(roles, sr.RoleCode)
			}
		} else {
			h.logger.Debug("auth/me: failed to get local roles", zap.Error(rErr))
		}

		if svcPerms, pErr := h.rbacService.GetUserPermissions(r.Context(), *tenantID, userID); pErr == nil {
			for _, sp := range svcPerms {
				permissions = appendUniqueStr(permissions, sp.PermissionCode)
			}
		} else {
			h.logger.Debug("auth/me: failed to get local permissions", zap.Error(pErr))
		}
	}

	// A tenant admin/owner must have full access to their tenant's inventory. Surface the
	// inventory_admin role (and superuser flag) explicitly so the UI shows every in-scope
	// page even before the JIT-provisioned local role row has fully propagated — the API
	// middleware already treats inventory_admin as a full bypass.
	isAdmin := claims.IsPlatformOwner || claims.IsSuperuser() ||
		rbac.IsAdminRoles(claims.Roles) || appendUniqueStrContains(roles, rbac.RoleInventoryAdmin)
	if isAdmin {
		roles = appendUniqueStr(roles, rbac.RoleInventoryAdmin)
	}

	// Resolve (and, when missing, auto-link) the user's home outlet so the UI never loads an
	// empty outlet while the tenant has one.
	outletID, outletName := h.resolveHomeOutlet(r.Context(), *tenantID, userID)

	respondJSON(w, http.StatusOK, MeResponse{
		ID:              claims.Subject,
		Email:           claims.Email,
		TenantID:        claims.TenantID,
		TenantSlug:      claims.GetTenantSlug(),
		Roles:           roles,
		Permissions:     permissions,
		IsPlatformOwner: claims.IsPlatformOwner,
		IsSuperUser:     claims.IsSuperuser() || isAdmin,
		OutletID:        outletID,
		OutletName:      outletName,
		EmailVerification: h.fetchEmailVerification(r.Context(), userID),
	})
}

// resolveHomeOutlet returns the user's home outlet (id + name) for the tenant. When the user
// has no outlet assignment yet, it links them to the tenant's default outlet (a warehouse with
// a non-nil outlet_id, preferring is_default) so login is never blocked and the UI always has a
// branch to load. Returns empty strings only when the tenant has no outlet-bearing warehouse.
func (h *AuthHandler) resolveHomeOutlet(ctx context.Context, tenantID, userID uuid.UUID) (string, string) {
	if h.orm == nil {
		return "", ""
	}

	// Already assigned? Prefer the home outlet, else the first assignment.
	rows, err := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(ctx)
	if err == nil && len(rows) > 0 {
		outletID := rows[0].OutletID
		for _, a := range rows {
			if a.IsHomeOutlet {
				outletID = a.OutletID
				break
			}
		}
		return outletID.String(), h.warehouseNameForOutlet(ctx, tenantID, outletID)
	}

	// Unassigned → link to the tenant default outlet.
	wh := h.defaultOutletWarehouse(ctx, tenantID)
	if wh == nil || wh.OutletID == nil {
		return "", ""
	}
	if _, cErr := h.orm.UserOutlet.Create().
		SetTenantID(tenantID).
		SetUserID(userID).
		SetOutletID(*wh.OutletID).
		SetIsHomeOutlet(true).
		Save(ctx); cErr != nil {
		// Non-fatal: still return the default outlet so the UI can load it this session.
		h.logger.Warn("auto-link default outlet failed", zap.Error(cErr),
			zap.String("tenant_id", tenantID.String()), zap.String("user_id", userID.String()))
	} else {
		h.logger.Info("auto-linked user to default outlet",
			zap.String("tenant_id", tenantID.String()), zap.String("user_id", userID.String()),
			zap.String("outlet_id", wh.OutletID.String()))
	}
	return wh.OutletID.String(), wh.Name
}

// defaultOutletWarehouse returns the tenant's default outlet-bearing warehouse: an active
// warehouse with a non-nil outlet_id, preferring is_default. nil when none qualifies.
func (h *AuthHandler) defaultOutletWarehouse(ctx context.Context, tenantID uuid.UUID) *ent.Warehouse {
	base := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.IsActive(true), entwarehouse.OutletIDNotNil())
	if wh, err := base.Clone().Where(entwarehouse.IsDefault(true)).First(ctx); err == nil && wh != nil {
		return wh
	}
	if wh, err := base.First(ctx); err == nil && wh != nil {
		return wh
	}
	return nil
}

// warehouseNameForOutlet resolves a warehouse display name from its outlet id.
func (h *AuthHandler) warehouseNameForOutlet(ctx context.Context, tenantID, outletID uuid.UUID) string {
	if wh, err := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletID(outletID)).
		First(ctx); err == nil && wh != nil {
		return wh.Name
	}
	return ""
}

// appendUniqueStrContains reports whether slice already contains s.
func appendUniqueStrContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// RegisterAuthRoutes mounts auth routes on the supplied router.
func (h *AuthHandler) RegisterAuthRoutes(r chi.Router) {
	r.Get("/auth/me", h.Me)
	r.Post("/auth/verify-email/send-code", h.SendMyEmailCode)
	r.Post("/auth/verify-email/verify-code", h.VerifyMyEmailCode)
}

// SendMyEmailCode proxies the embedded verify-email dialog's "send code" action to auth-api.
// POST /auth/verify-email/send-code  body: {email}
func (h *AuthHandler) SendMyEmailCode(w http.ResponseWriter, r *http.Request) {
	h.proxyEmailCode(w, r, "send-code")
}

// VerifyMyEmailCode proxies the embedded verify-email dialog's "verify code" action to auth-api.
// POST /auth/verify-email/verify-code  body: {email, code}
func (h *AuthHandler) VerifyMyEmailCode(w http.ResponseWriter, r *http.Request) {
	h.proxyEmailCode(w, r, "verify-code")
}

// proxyEmailCode forwards the shared VerifyEmailBanner's send/verify-code call to auth-api's S2S
// endpoint (INTERNAL_SERVICE_KEY), resolving the real auth-api user id from the CALLER'S OWN
// claims — the same id regardless of whether they authenticated via SSO or a local terminal/PIN
// JWT. That distinction is the reason this proxy exists at all: the embedded dialog used to POST
// straight to auth-api with the user's session token as a Bearer credential, which works for an
// SSO session (a real auth-api-signed JWT) but can NEVER work for a terminal/PIN session (signed
// with inventory-api's own HMAC secret) — auth-api has no key to verify a token it didn't sign,
// so every PIN-logged-in user got a hard "missing or invalid auth" with no code ever sent. Routing
// through inventory-api's own RequireAnyAuth (which already accepts both token kinds) and forwarding
// S2S fixes both session kinds uniformly. The response status and body are relayed back unchanged
// so the UI sees the exact error/success shape auth-api itself returned.
//
// Deliberately named /auth/verify-email/*, NOT /auth/me/email/*: apiClient's 401 handler skips
// its refresh-and-retry for any URL containing "/auth/me", so keeping that substring out of this
// path is what lets an expired-but-refreshable token recover silently instead of forcing a logout.
func (h *AuthHandler) proxyEmailCode(w http.ResponseWriter, r *http.Request, action string) {
	if h.authURL == "" || h.internalKey == "" {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "email verification not configured")
		return
	}
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing claims")
		return
	}
	userID, err := claims.UserID()
	if err != nil || userID == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid user ID in token")
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	// Send with the user's real tenant id so notifications-api can resolve tenant branding
	// (a nil tenant makes the tenant resolver fail and strips branding).
	if action == "send-code" {
		if tenantID, terr := claims.TenantUUID(); terr == nil && tenantID != nil {
			body["tenant_id"] = tenantID.String()
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON payload")
		return
	}

	url := strings.TrimRight(h.authURL, "/") + "/api/v1/s2s/users/" + userID.String() + "/email/" + action
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.internalKey)
	resp, err := h.http.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "could not reach auth service")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// appendUniqueStr appends s to slice only if not already present.
func appendUniqueStr(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
