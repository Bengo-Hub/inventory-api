// Package treasury is a service-to-service client for treasury-api, the platform
// source of truth for tax codes and rates. Inventory never stores tax rates locally;
// it resolves them from treasury at read time and caches them briefly.
package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const taxCodesTTL = 10 * time.Minute

// flexFloat unmarshals a JSON number OR a quoted numeric string — treasury-api encodes
// decimal rates as strings (e.g. "16") in some responses.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("treasury: parse rate %q: %w", s, err)
	}
	*f = flexFloat(v)
	return nil
}

// TaxCode mirrors treasury-api's S2S TaxCodeModel.
type TaxCode struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Rate      flexFloat `json:"rate"`
	TaxType   string    `json:"tax_type"`
	KraCode   string    `json:"kra_code"`
	IsDefault bool      `json:"is_default"`
	IsActive  bool      `json:"is_active"`
}

// Client calls treasury-api over S2S (X-API-Key: INTERNAL_SERVICE_KEY).
type Client struct {
	baseURL string
	apiKey  string
	cache   *sharedcache.Aside
	http    *http.Client
	log     *zap.Logger
}

// NewClient builds a treasury S2S client. apiKey is the shared INTERNAL_SERVICE_KEY.
func NewClient(baseURL, apiKey string, cache *sharedcache.Aside, log *zap.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		cache:   cache,
		http:    &http.Client{Timeout: 8 * time.Second},
		log:     log.Named("treasury.client"),
	}
}

// Enabled reports whether the client is configured for S2S calls.
func (c *Client) Enabled() bool { return c.baseURL != "" && c.apiKey != "" }

// GetTaxCodes returns the tenant's active tax codes from treasury-api (cached ~10m).
// Returns nil when the client is not configured. tenantID MUST be the tenant UUID
// (the treasury S2S group does not resolve slugs).
func (c *Client) GetTaxCodes(ctx context.Context, tenantID uuid.UUID) ([]TaxCode, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if c.cache != nil {
		key := sharedcache.Key("inventory", "treasury", "taxcodes", tenantID.String())
		return sharedcache.GetOrSet(ctx, c.cache, key, taxCodesTTL, func(ctx context.Context) ([]TaxCode, error) {
			return c.fetchTaxCodes(ctx, tenantID)
		})
	}
	return c.fetchTaxCodes(ctx, tenantID)
}

func (c *Client) fetchTaxCodes(ctx context.Context, tenantID uuid.UUID) ([]TaxCode, error) {
	url := fmt.Sprintf("%s/api/v1/s2s/%s/taxes", c.baseURL, tenantID.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("treasury: get tax codes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("treasury: get tax codes: status %d", resp.StatusCode)
	}
	var out struct {
		TaxCodes []TaxCode `json:"tax_codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("treasury: decode tax codes: %w", err)
	}
	return out.TaxCodes, nil
}

// VATActive reports whether the tenant should charge VAT (treasury tax profile). Defaults
// TRUE on any error / when unconfigured so we never silently stop a registered business from
// charging VAT; returns false only when treasury affirmatively says the business is not
// VAT-registered. Cached ~10m.
func (c *Client) VATActive(ctx context.Context, tenantID uuid.UUID) bool {
	if !c.Enabled() {
		return true
	}
	fetch := func(ctx context.Context) (bool, error) {
		url := fmt.Sprintf("%s/api/v1/s2s/%s/tax-profile", c.baseURL, tenantID.String())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return true, err
		}
		req.Header.Set("X-API-Key", c.apiKey)
		resp, err := c.http.Do(req)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return true, fmt.Errorf("treasury: tax-profile status %d", resp.StatusCode)
		}
		var out struct {
			VATActive bool `json:"vat_active"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return true, err
		}
		return out.VATActive, nil
	}
	if c.cache != nil {
		key := sharedcache.Key("inventory", "treasury", "vatactive", tenantID.String())
		v, err := sharedcache.GetOrSet(ctx, c.cache, key, taxCodesTTL, fetch)
		if err != nil {
			return true
		}
		return v
	}
	v, err := fetch(ctx)
	if err != nil {
		return true
	}
	return v
}

// InvalidateTaxCache deletes the cached treasury tax data for a tenant (the tax-code
// list and the VAT-active switch). Call it when treasury publishes a tax-code change so
// fresh rates propagate immediately instead of waiting for the cache TTL. Safe no-op when
// caching is disabled. The cache keys are kept in sync with GetTaxCodes / VATActive.
func (c *Client) InvalidateTaxCache(ctx context.Context, tenantID uuid.UUID) {
	if c.cache == nil {
		return
	}
	c.cache.Invalidate(ctx,
		sharedcache.Key("inventory", "treasury", "taxcodes", tenantID.String()),
		sharedcache.Key("inventory", "treasury", "vatactive", tenantID.String()),
	)
	c.log.Debug("invalidated treasury tax cache", zap.String("tenant", tenantID.String()))
}

// ResolveVATRate returns the VAT rate (percent) and the tax code used, selecting by:
// preferred code → is_default → first tax_type=="vat". ok=false when treasury is
// unavailable or no VAT code exists (caller should apply its own fallback).
func (c *Client) ResolveVATRate(ctx context.Context, tenantID uuid.UUID, preferredCode string) (rate float64, code string, ok bool) {
	codes, err := c.GetTaxCodes(ctx, tenantID)
	if err != nil {
		c.log.Debug("resolve vat rate: treasury unavailable", zap.String("tenant", tenantID.String()), zap.Error(err))
		return 0, "", false
	}
	preferredCode = strings.TrimSpace(preferredCode)
	var byDefault, byVAT *TaxCode
	for i := range codes {
		t := &codes[i]
		if !t.IsActive {
			continue
		}
		if preferredCode != "" && strings.EqualFold(t.Code, preferredCode) {
			return float64(t.Rate), t.Code, true
		}
		if t.IsDefault && byDefault == nil {
			byDefault = t
		}
		if strings.EqualFold(t.TaxType, "vat") && byVAT == nil {
			byVAT = t
		}
	}
	if byDefault != nil {
		return float64(byDefault.Rate), byDefault.Code, true
	}
	if byVAT != nil {
		return float64(byVAT.Rate), byVAT.Code, true
	}
	return 0, "", false
}
