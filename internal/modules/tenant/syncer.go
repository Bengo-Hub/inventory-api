package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
)

// Syncer handles dynamic syncing of tenant data from auth-api.
type Syncer struct {
	client *ent.Client
}

// NewSyncer creates a new TenantSyncer.
func NewSyncer(client *ent.Client) *Syncer {
	return &Syncer{client: client}
}

// authAPITenantResponse is the full tenant JSON response from GET /api/v1/tenants/by-slug/{slug}.
type authAPITenantResponse struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Slug               string         `json:"slug"`
	Status             string         `json:"status"`
	ContactEmail       string         `json:"contact_email,omitempty"`
	ContactPhone       string         `json:"contact_phone,omitempty"`
	LogoURL            string         `json:"logo_url,omitempty"`
	Website            string         `json:"website,omitempty"`
	Country            string         `json:"country,omitempty"`
	Timezone           string         `json:"timezone,omitempty"`
	BrandColors        map[string]any `json:"brand_colors,omitempty"`
	OrgSize            string         `json:"org_size,omitempty"`
	UseCase            string         `json:"use_case,omitempty"`
	SubscriptionPlan   string         `json:"subscription_plan,omitempty"`
	SubscriptionStatus string         `json:"subscription_status,omitempty"`
	TierLimits         map[string]any `json:"tier_limits,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// SyncTenant fetches the FULL tenant record from auth-api and persists it
// in the local DB with the same UUID as auth-api. Used for JIT provisioning.
func (s *Syncer) SyncTenant(ctx context.Context, slug string) (uuid.UUID, error) {
	// Fast path: check if tenant already exists locally
	existingFast, err := s.client.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if err == nil && existingFast != nil {
		return existingFast.ID, nil
	}

	authAPIURL := os.Getenv("AUTH_API_URL")
	if authAPIURL == "" {
		authAPIURL = "https://sso.codevertexitsolutions.com"
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

	extMeta := map[string]any{}
	for k, v := range remote.Metadata {
		extMeta[k] = v
	}

	existing, queryErr := s.client.Tenant.Query().Where(enttenant.IDEQ(realID)).Only(ctx)
	if queryErr == nil && existing != nil {
		_, updErr := existing.Update().
			SetName(remote.Name).
			SetStatus(remote.Status).
			SetContactEmail(remote.ContactEmail).
			SetContactPhone(remote.ContactPhone).
			SetLogoURL(remote.LogoURL).
			SetWebsite(remote.Website).
			SetCountry(remote.Country).
			SetTimezone(remote.Timezone).
			SetBrandColors(remote.BrandColors).
			SetOrgSize(remote.OrgSize).
			SetUseCase(remote.UseCase).
			SetSubscriptionPlan(remote.SubscriptionPlan).
			SetSubscriptionStatus(remote.SubscriptionStatus).
			SetTierLimits(remote.TierLimits).
			SetMetadata(extMeta).
			Save(ctx)
		if updErr != nil {
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

	created, createErr := s.client.Tenant.Create().
		SetID(realID).
		SetSlug(remote.Slug).
		SetName(remote.Name).
		SetStatus(remote.Status).
		SetContactEmail(remote.ContactEmail).
		SetContactPhone(remote.ContactPhone).
		SetLogoURL(remote.LogoURL).
		SetWebsite(remote.Website).
		SetCountry(remote.Country).
		SetTimezone(remote.Timezone).
		SetBrandColors(remote.BrandColors).
		SetOrgSize(remote.OrgSize).
		SetUseCase(remote.UseCase).
		SetSubscriptionPlan(remote.SubscriptionPlan).
		SetSubscriptionStatus(remote.SubscriptionStatus).
		SetTierLimits(remote.TierLimits).
		SetMetadata(extMeta).
		Save(ctx)

	if createErr != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: create tenant: %w", createErr)
	}

	log.Printf("  [tenant-sync] dynamically created %s (UUID %s, synced from auth-api)", slug, created.ID)
	return created.ID, nil
}
