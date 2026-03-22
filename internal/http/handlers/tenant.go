package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
)

// tenantDB is set during handler initialization for slug-to-UUID resolution.
var tenantDB *ent.Client

// SetTenantDB sets the ent client used for resolving tenant slugs to UUIDs.
func SetTenantDB(client *ent.Client) {
	tenantDB = client
}

// ResolveTenantForRequest resolves the target tenant UUID from the request,
// following the platform-owner override pattern:
//
//  1. Platform owners: check ?tenantId= query param (allows cross-tenant access)
//  2. httpware context (set by TenantV2 middleware from headers/JWT/URL)
//  3. URL path param {tenant}
//  4. JWT claims fallback
//
// Returns (uuid.Nil, true) when the caller is a platform owner and no specific
// tenant was requested — the handler should return data for ALL tenants.
// Returns (uuid.Nil, false) when tenant resolution fails entirely.
func ResolveTenantForRequest(r *http.Request) (uuid.UUID, bool) {
	ctx := r.Context()
	isPO := httpware.IsPlatformOwner(ctx)

	// 1. Platform owner query-param override
	if isPO {
		if q := r.URL.Query().Get("tenantId"); q != "" {
			if id, err := uuid.Parse(q); err == nil {
				return id, true
			}
		}
	}

	// 2. httpware context (from TenantV2 middleware)
	if tenantIDStr := httpware.GetTenantID(ctx); tenantIDStr != "" {
		if id, err := uuid.Parse(tenantIDStr); err == nil {
			if isPO {
				claims, ok := authclient.ClaimsFromContext(ctx)
				if ok && claims.TenantID == tenantIDStr {
					return uuid.Nil, true // platform owner's own tenant → all
				}
			}
			return id, true
		}
	}

	// 3. URL path parameter {tenant}
	if param := chi.URLParam(r, "tenant"); param != "" {
		if id, err := uuid.Parse(param); err == nil {
			return id, true
		}
	}

	// 4. JWT claims fallback
	claims, found := authclient.ClaimsFromContext(ctx)
	if found && claims.TenantID != "" {
		if id, err := uuid.Parse(claims.TenantID); err == nil {
			if isPO {
				return uuid.Nil, true
			}
			return id, true
		}
	}

	if isPO {
		return uuid.Nil, true
	}
	return uuid.Nil, false
}

// parseTenantID resolves tenant UUID using the shared platform-owner-aware resolver.
// Falls back to slug-to-UUID lookup via the tenant table for unauthenticated requests.
func parseTenantID(r *http.Request) (uuid.UUID, error) {
	id, ok := ResolveTenantForRequest(r)
	if ok && id != uuid.Nil {
		return id, nil
	}

	// Fallback: resolve slug from URL or context to UUID via tenant table
	slug := httpware.GetTenantSlug(r.Context())
	if slug == "" {
		// Try URL path param as slug
		if param := chi.URLParam(r, "tenant"); param != "" {
			if _, err := uuid.Parse(param); err != nil {
				slug = param // Not a UUID, treat as slug
			}
		}
	}

	if slug != "" && tenantDB != nil {
		resolved, err := resolveTenantBySlug(r.Context(), slug)
		if err == nil {
			return resolved, nil
		}
	}

	if ok {
		return id, nil // uuid.Nil for platform owner (all tenants)
	}
	return uuid.Nil, errors.New("tenant context required")
}

// resolveTenantBySlug looks up tenant UUID from slug in the local tenant table.
func resolveTenantBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	t, err := tenantDB.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return t.ID, nil
}
