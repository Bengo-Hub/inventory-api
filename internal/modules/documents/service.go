package documents

import (
	"context"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
)

// Service is the facade for inventory-api document generation: tenant-branding
// resolution (from the auth cache) + per-tenant document numbering. PDF rendering
// is done by the stateless Render* functions in this package.
type Service struct {
	ent     *ent.Client
	cache   *sharedcache.Aside
	authURL string
	seq     *SequenceService
	log     *zap.Logger
}

// NewService creates the documents Service.
func NewService(client *ent.Client, cache *sharedcache.Aside, authURL string, log *zap.Logger) *Service {
	return &Service{
		ent:     client,
		cache:   cache,
		authURL: authURL,
		seq:     NewSequenceService(client),
		log:     log.Named("documents.service"),
	}
}

// Seq exposes the per-tenant document-number generator.
func (s *Service) Seq() *SequenceService { return s.seq }

// GetBranding resolves tenant branding (logo, name, contacts) from the auth cache.
// Always returns a usable Branding (degrades gracefully on cache/auth failure).
func (s *Service) GetBranding(ctx context.Context, tenantID uuid.UUID) Branding {
	b := Branding{}
	t, err := s.ent.Tenant.Query().Where(enttenant.ID(tenantID)).Only(ctx)
	if err != nil {
		s.log.Warn("documents: tenant lookup failed", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		return b
	}
	b.CompanyName = t.Name
	if s.cache == nil || s.authURL == "" {
		return b
	}
	td, err := sharedcache.GetTenantDetails(ctx, s.cache, s.authURL, t.Slug, sharedcache.DefaultTenantTTL)
	if err != nil {
		s.log.Warn("documents: branding fetch failed", zap.String("slug", t.Slug), zap.Error(err))
		return b
	}
	if td.Name != "" {
		b.CompanyName = td.Name
	}
	b.LogoURL = td.LogoURL
	b.Email = td.ContactEmail
	b.Phone = td.ContactPhone
	b.Website = td.Website
	// Build a human address block from tenant metadata (street, city, postal, country),
	// falling back to the country code so the document always shows a location.
	b.Address = addressLines(td.Metadata, td.Country)
	// Tagline + KRA PIN come from tenant metadata (the cache lib doesn't expose tax_pin directly).
	b.Tagline = metaString(td.Metadata, "tagline")
	b.KRAPIN = metaString(td.Metadata, "kra_pin")
	return b
}

// metaString reads a string value from a tenant metadata map (empty when absent).
func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta[key].(string); ok {
		return v
	}
	return ""
}

// addressLines assembles up to three display lines from tenant metadata:
//   line 1: street address
//   line 2: postal · city
//   line 3: country (prefers a human country_name over the ISO code)
func addressLines(meta map[string]any, country string) []string {
	get := func(k string) string {
		if meta == nil {
			return ""
		}
		if v, ok := meta[k].(string); ok {
			return v
		}
		return ""
	}
	var lines []string
	if street := get("address"); street != "" {
		lines = append(lines, street)
	}
	cityLine := joinNonEmpty("  ·  ", get("postal_code"), get("city"))
	if cityLine != "" {
		lines = append(lines, cityLine)
	}
	if cn := get("country_name"); cn != "" {
		lines = append(lines, cn)
	} else if country != "" {
		lines = append(lines, country)
	}
	return lines
}

func joinNonEmpty(sep string, parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}
