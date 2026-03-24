package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
)

// Syncer handles dynamic syncing of tenant data from auth-api.
type Syncer struct {
	client  *ent.Client
	authURL string
}

// NewSyncer creates a new TenantSyncer.
// authURL is the base URL of the auth-api (e.g. from AUTH_SERVICE_URL config).
func NewSyncer(client *ent.Client, authURL string) *Syncer {
	return &Syncer{client: client, authURL: authURL}
}

// authAPITenantResponse represents the tenant JSON from GET /api/v1/tenants/by-slug/{slug}.
// We only store minimal fields locally; branding, contact info, and subscription data
// are owned by auth-api and fetched on demand or read from JWT claims.
type authAPITenantResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Status  string `json:"status"`
	UseCase string `json:"use_case,omitempty"`
}

// SyncTenant fetches the tenant record from auth-api and persists only the
// minimal reference fields locally (id, name, slug, status, use_case).
// Branding, contact info, and subscription data remain in auth-api only.
func (s *Syncer) SyncTenant(ctx context.Context, slug string) (uuid.UUID, error) {
	// Fast path: check if tenant already exists locally
	existingFast, err := s.client.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if err == nil && existingFast != nil {
		return existingFast.ID, nil
	}

	authAPIURL := s.authURL
	if envURL := os.Getenv("AUTH_API_URL"); envURL != "" {
		authAPIURL = envURL
	}
	endpoint := strings.TrimRight(authAPIURL, "/") + "/api/v1/tenants/by-slug/" + slug

	log.Printf("  [tenant-sync] dynamically fetching %s from %s", slug, endpoint)
	resp, err := http.Get(endpoint) //nolint:noctx
	if err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: tenant %q not found (404)", slug)
	}
	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: auth-api HTTP %d for %q", resp.StatusCode, slug)
	}

	var remote authAPITenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&remote); err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: decode response: %w", err)
	}
	realID, err := uuid.Parse(remote.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: invalid UUID %q: %w", remote.ID, err)
	}

	now := time.Now()

	existing, queryErr := s.client.Tenant.Query().Where(enttenant.IDEQ(realID)).Only(ctx)
	if queryErr == nil && existing != nil {
		update := existing.Update().
			SetName(remote.Name).
			SetStatus(remote.Status).
			SetSyncStatus("synced").
			SetLastSyncAt(now)
		if remote.UseCase != "" {
			update = update.SetUseCase(remote.UseCase)
		}
		if _, updErr := update.Save(ctx); updErr != nil {
			return uuid.Nil, fmt.Errorf("tenant.Syncer: update tenant: %w", updErr)
		}
		log.Printf("  [tenant-sync] updated %s (UUID %s) from auth-api", slug, realID)
		return realID, nil
	}

	if !ent.IsNotFound(queryErr) {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: query existing: %w", queryErr)
	}

	bySlug, _ := s.client.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if bySlug != nil && bySlug.ID != realID {
		log.Printf("  [WARN] tenant %q exists locally with UUID %s but auth-api says %s", slug, bySlug.ID, realID)
		return bySlug.ID, nil
	}

	create := s.client.Tenant.Create().
		SetID(realID).
		SetSlug(remote.Slug).
		SetName(remote.Name).
		SetStatus(remote.Status).
		SetSyncStatus("synced").
		SetLastSyncAt(now)
	if remote.UseCase != "" {
		create = create.SetUseCase(remote.UseCase)
	}
	created, createErr := create.Save(ctx)
	if createErr != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: create tenant: %w", createErr)
	}

	log.Printf("  [tenant-sync] dynamically created %s (UUID %s, synced from auth-api)", slug, created.ID)
	return created.ID, nil
}
