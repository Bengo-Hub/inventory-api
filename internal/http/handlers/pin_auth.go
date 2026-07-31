package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

// PINAuthHandler implements terminal/PIN login that supplements SSO for warehouse/desk
// terminals. An outlet is chosen first, then a 4-digit PIN; staff may only log into an outlet
// they're assigned to (admins → any outlet). Lockout after repeated wrong PINs. PIN hashes are
// provisioned from auth.user.pin_set events (service = "inventory").
type PINAuthHandler struct {
	orm       *ent.Client
	rbac      *rbac.Service
	subs      *subscriptions.Client
	jwtSecret []byte
	log       *zap.Logger
}

// NewPINAuthHandler builds the PIN auth handler.
func NewPINAuthHandler(orm *ent.Client, rbacSvc *rbac.Service, subs *subscriptions.Client, jwtSecret string, log *zap.Logger) *PINAuthHandler {
	return &PINAuthHandler{orm: orm, rbac: rbacSvc, subs: subs, jwtSecret: []byte(jwtSecret), log: log.Named("pin_auth")}
}

// Secret exposes the HMAC secret so the router can build RequireAnyAuth.
func (h *PINAuthHandler) Secret() []byte { return h.jwtSecret }

// PIN lockout policy (mirrors pos-api / library-api).
const (
	maxFailedPINAttempts = 5
	pinLockoutDuration   = 15 * time.Minute
)

func (h *PINAuthHandler) tenantBySlug(r *http.Request) (*ent.Tenant, bool) {
	slug := chi.URLParam(r, "tenant")
	if slug == "" {
		slug = chi.URLParam(r, "tenantID")
	}
	t, err := h.orm.Tenant.Query().Where(enttenant.Slug(slug)).Only(r.Context())
	if err != nil {
		return nil, false
	}
	return t, true
}

func roleCodes(roles []*rbac.InventoryRole) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.RoleCode)
	}
	return out
}

func isInventoryAdmin(roles []string) bool {
	for _, r := range roles {
		if r == rbac.RoleInventoryAdmin || r == rbac.RoleWarehouseManager {
			return true
		}
	}
	return false
}

// outletAllowed reports whether the user may log in to outletID. Admins → any outlet; a user
// with zero outlet assignments is unrestricted (progressive rollout, mirrors EnforceOutletAssignment);
// otherwise the outlet must be among their assignments.
func (h *PINAuthHandler) outletAllowed(ctx context.Context, tenantID, userID uuid.UUID, roles []string, outletID uuid.UUID) bool {
	if isInventoryAdmin(roles) || outletID == uuid.Nil {
		return true
	}
	rows, err := h.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(ctx)
	if err != nil || len(rows) == 0 {
		return true
	}
	for _, a := range rows {
		if a.OutletID == outletID {
			return true
		}
	}
	return false
}

// terminalClaimsFor builds the JWT claims for a PIN session (roles, permissions, subscription).
func (h *PINAuthHandler) terminalClaimsFor(ctx context.Context, t *ent.Tenant, u *ent.InventoryUser, outletID uuid.UUID) terminalClaims {
	roles, _ := h.rbac.GetUserRoles(ctx, t.ID, u.ID)
	perms, _ := h.rbac.GetUserPermissions(ctx, t.ID, u.ID)
	roleCodesList := roleCodes(roles)
	permCodes := make([]string, 0, len(perms))
	for _, p := range perms {
		permCodes = append(permCodes, p.PermissionCode)
	}
	tc := terminalClaims{
		UserID:             u.AuthServiceUserID.String(),
		TenantID:           t.ID.String(),
		TenantSlug:         t.Slug,
		Email:              u.Email,
		Name:               u.Name,
		Roles:              roleCodesList,
		Permissions:        permCodes,
		SubscriptionStatus: "active",
	}
	if outletID != uuid.Nil {
		tc.OutletID = outletID.String()
	}
	// Demo/platform bypass by slug so PIN sessions can use feature-gated routes even if the
	// subscriptions S2S call is unavailable (mirrors pos-api / library-api).
	tc.IsDemo = t.Slug == "codevertex-demo"
	tc.IsPlatformOwner = t.Slug == "codevertex"
	if h.subs != nil {
		if e := h.subs.GetEntitlements(ctx, t.ID.String()); e != nil {
			tc.SubscriptionStatus = e.Status
			tc.SubscriptionFeatures = e.Features
			tc.BillingMode = e.BillingMode
			tc.IsDemo = tc.IsDemo || e.IsDemoBypass
		}
	}
	return tc
}

// loginResponse issues the terminal JWT and builds the success body.
func (h *PINAuthHandler) loginResponse(ctx context.Context, t *ent.Tenant, u *ent.InventoryUser, wh *ent.Warehouse) (map[string]any, error) {
	var outletID uuid.UUID
	if wh != nil && wh.OutletID != nil {
		outletID = *wh.OutletID
	}
	tc := h.terminalClaimsFor(ctx, t, u, outletID)
	token, err := issueTerminalJWT(h.jwtSecret, tc)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"access_token": token,
		"token_type":   "terminal",
		"user_id":      u.AuthServiceUserID.String(),
		"name":         u.Name,
		"email":        u.Email,
		"roles":        tc.Roles,
		"permissions":  tc.Permissions,
		"is_admin":     isInventoryAdmin(tc.Roles),
		"expires_in":   int(terminalTokenTTL.Seconds()),
	}
	if wh != nil {
		if wh.OutletID != nil {
			body["outlet_id"] = wh.OutletID.String()
		}
		body["outlet_name"] = wh.Name
		body["outlet_use_case"] = wh.UseCase
	}
	return body, nil
}

// activeOutletWarehouses returns the tenant's active outlet-bearing warehouses.
func (h *PINAuthHandler) activeOutletWarehouses(ctx context.Context, tenantID uuid.UUID) []*ent.Warehouse {
	rows, _ := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.IsActive(true), entwarehouse.OutletIDNotNil()).
		Order(ent.Desc(entwarehouse.FieldIsDefault)).
		All(ctx)
	return rows
}

func (h *PINAuthHandler) warehouseByOutlet(ctx context.Context, tenantID, outletID uuid.UUID) *ent.Warehouse {
	wh, err := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletID(outletID)).
		First(ctx)
	if err != nil {
		return nil
	}
	return wh
}

// PINOutlets lists active outlets for the PIN-login outlet picker (public per tenant).
func (h *PINAuthHandler) PINOutlets(w http.ResponseWriter, r *http.Request) {
	t, ok := h.tenantBySlug(r)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown tenant")
		return
	}
	rows := h.activeOutletWarehouses(r.Context(), t.ID)
	out := make([]map[string]any, 0, len(rows))
	for _, wh := range rows {
		item := map[string]any{"name": wh.Name, "code": wh.Code, "is_default": wh.IsDefault, "use_case": wh.UseCase}
		if wh.OutletID != nil {
			item["id"] = wh.OutletID.String()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "total": len(out)})
}

type identifyByPINInput struct {
	PIN      string `json:"pin"`
	OutletID string `json:"outlet_id"`
}

// IdentifyByPIN authenticates by PIN alone (no user picked): scans active PIN-holding staff at
// the outlet and matches by bcrypt. Body: { pin, outlet_id }.
func (h *PINAuthHandler) IdentifyByPIN(w http.ResponseWriter, r *http.Request) {
	if len(h.jwtSecret) == 0 {
		writeError(w, http.StatusServiceUnavailable, "PIN_DISABLED", "PIN login is not configured")
		return
	}
	t, ok := h.tenantBySlug(r)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown tenant")
		return
	}
	var body identifyByPINInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.PIN) < 4 {
		writeError(w, http.StatusBadRequest, "INVALID_PIN", "a valid PIN is required")
		return
	}
	outletID, _ := uuid.Parse(body.OutletID)

	users, err := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(t.ID), entinvuser.StatusEQ("active"), entinvuser.PinHashNotNil()).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "failed to load staff")
		return
	}

	now := time.Now()
	for _, u := range users {
		if u.PinHash == nil || *u.PinHash == "" {
			continue
		}
		if u.PinLockedUntil != nil && u.PinLockedUntil.After(now) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(*u.PinHash), []byte(body.PIN)) != nil {
			continue
		}
		// Match found — enforce outlet assignment.
		roles, _ := h.rbac.GetUserRoles(r.Context(), t.ID, u.ID)
		if !h.outletAllowed(r.Context(), t.ID, u.ID, roleCodes(roles), outletID) {
			writeError(w, http.StatusForbidden, "OUTLET_NOT_ASSIGNED", "You are not assigned to this outlet")
			return
		}
		if u.PinFailedAttempts > 0 {
			_, _ = u.Update().SetPinFailedAttempts(0).ClearPinLockedUntil().Save(r.Context())
		}
		wh := h.warehouseByOutlet(r.Context(), t.ID, outletID)
		resp, lErr := h.loginResponse(r.Context(), t, u, wh)
		if lErr != nil {
			writeError(w, http.StatusInternalServerError, "TOKEN_FAILED", "failed to issue session")
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	writeError(w, http.StatusUnauthorized, "INVALID_PIN", "Incorrect PIN")
}

type pinLoginInput struct {
	UserID   string `json:"user_id"`
	PIN      string `json:"pin"`
	OutletID string `json:"outlet_id"`
}

// Login authenticates a specific user by PIN. Body: { user_id, pin, outlet_id }.
func (h *PINAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if len(h.jwtSecret) == 0 {
		writeError(w, http.StatusServiceUnavailable, "PIN_DISABLED", "PIN login is not configured")
		return
	}
	t, ok := h.tenantBySlug(r)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown tenant")
		return
	}
	var body pinLoginInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" || len(body.PIN) < 4 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "user_id and a valid PIN are required")
		return
	}
	authUID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER", "invalid user_id")
		return
	}
	outletID, _ := uuid.Parse(body.OutletID)

	u, err := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(t.ID), entinvuser.AuthServiceUserID(authUID)).
		Only(r.Context())
	if err != nil || u.PinHash == nil || *u.PinHash == "" {
		writeError(w, http.StatusUnauthorized, "INVALID_PIN", "PIN login not available for this user")
		return
	}
	if u.PinLockedUntil != nil && u.PinLockedUntil.After(time.Now()) {
		writeError(w, http.StatusTooManyRequests, "PIN_LOCKED", "Too many attempts — try again later")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PinHash), []byte(body.PIN)) != nil {
		h.registerPINFailure(r.Context(), u)
		writeError(w, http.StatusUnauthorized, "INVALID_PIN", "Incorrect PIN")
		return
	}
	roles, _ := h.rbac.GetUserRoles(r.Context(), t.ID, u.ID)
	if !h.outletAllowed(r.Context(), t.ID, u.ID, roleCodes(roles), outletID) {
		writeError(w, http.StatusForbidden, "OUTLET_NOT_ASSIGNED", "You are not assigned to this outlet")
		return
	}
	if u.PinFailedAttempts > 0 {
		_, _ = u.Update().SetPinFailedAttempts(0).ClearPinLockedUntil().Save(r.Context())
	}
	wh := h.warehouseByOutlet(r.Context(), t.ID, outletID)
	resp, lErr := h.loginResponse(r.Context(), t, u, wh)
	if lErr != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_FAILED", "failed to issue session")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type setPINInput struct {
	UserID string `json:"user_id"`
	PIN    string `json:"pin"`
}

// SetPIN lets an inventory admin/manager set or replace a staff member's terminal PIN
// from inventory-ui's Team page — a self-service alternative to the centralized auth-ui
// admin screens (mirrors library-api's PINAuthHandler.SetPIN). Body: { user_id, pin }.
// user_id here is the local InventoryUser ID (as returned by GET /users and used by the
// other Team-page endpoints like the outlet-assignment routes above), NOT the
// auth-service user id that Login/IdentifyByPIN match against — inventory-ui's Team page
// only ever has the local ID on hand. Registered in the authenticated route group and
// gated by rbac.PermUsersManage (see router.go).
func (h *PINAuthHandler) SetPIN(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "invalid tenant")
		return
	}
	var body setPINInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" || len(body.PIN) < 4 {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "user_id and a 4+ digit pin are required")
		return
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER", "invalid user_id")
		return
	}
	u, err := h.orm.InventoryUser.Query().
		Where(entinvuser.TenantID(tenantID), entinvuser.ID(userID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.PIN), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "HASH_FAILED", err.Error())
		return
	}
	if _, err := u.Update().
		SetPinHash(string(hash)).
		SetPinFailedAttempts(0).
		ClearPinLockedUntil().
		Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_FAILED", "failed to save pin")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

func (h *PINAuthHandler) registerPINFailure(ctx context.Context, u *ent.InventoryUser) {
	attempts := u.PinFailedAttempts + 1
	upd := u.Update().SetPinFailedAttempts(attempts)
	if attempts >= maxFailedPINAttempts {
		upd = upd.SetPinLockedUntil(time.Now().Add(pinLockoutDuration)).SetPinFailedAttempts(0)
	}
	_, _ = upd.Save(ctx)
}
