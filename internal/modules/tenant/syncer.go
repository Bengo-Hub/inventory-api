package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

// driftProbeClient carries a short timeout so the per-call drift check against auth-api can
// never hang a request path; on timeout the caller falls back to the local projection.
var driftProbeClient = &http.Client{Timeout: 5 * time.Second}

// driftCheckInterval throttles the auth-api drift probe. SyncTenant runs from request
// middleware on EVERY API call, so probing per call would put an auth-api round trip in
// front of the whole service. Drift only happens when a tenant is deleted and recreated
// upstream, so checking a given slug a few times an hour catches it soon enough.
const driftCheckInterval = 10 * time.Minute

// Syncer handles dynamic syncing of tenant data from auth-api.
type Syncer struct {
	client  *ent.Client
	authURL string
	db      *sql.DB

	driftMu       sync.Mutex
	lastDriftScan map[string]time.Time
}

// NewSyncer creates a new TenantSyncer.
// authURL is the base URL of the auth-api (e.g. from AUTH_SERVICE_URL config).
func NewSyncer(client *ent.Client, authURL string) *Syncer {
	return &Syncer{client: client, authURL: authURL}
}

// WithDB attaches the raw database handle the drift self-heal needs. Without it a detected
// tenant-UUID drift is reported but not repaired (the stale local UUID is still returned),
// because re-keying spans every tenant-scoped table and cannot be expressed through Ent.
func (s *Syncer) WithDB(db *sql.DB) *Syncer {
	s.db = db
	return s
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
	existingFast, fastErr := s.client.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	hasLocal := fastErr == nil && existingFast != nil

	authAPIURL := s.authURL
	if envURL := os.Getenv("AUTH_API_URL"); envURL != "" {
		authAPIURL = envURL
	}
	endpoint := strings.TrimRight(authAPIURL, "/") + "/api/v1/tenants/by-slug/" + slug

	// Drift guard: auth-api owns the tenant UUID. A locally cached row used to be returned
	// unconditionally, so when a tenant was deleted and recreated upstream (new UUID) this
	// service pinned the stale one forever — seed/slug-resolved writes landed under the old
	// UUID while event-driven writes landed under auth's, forking the tenant in half. Confirm
	// against auth before trusting the local row; if auth is unreachable, keep serving it.
	if hasLocal {
		if !s.dueForDriftScan(slug) {
			return existingFast.ID, nil
		}
		remoteID, probeErr := s.probeAuthTenantID(ctx, endpoint)
		if probeErr != nil {
			return existingFast.ID, nil // auth unreachable — the cached projection still stands
		}
		if remoteID == existingFast.ID {
			return existingFast.ID, nil
		}
		log.Printf("  [tenant-sync] DRIFT: %s is %s locally but %s in auth-api — adopting auth's UUID",
			slug, existingFast.ID, remoteID)
		return s.adoptAuthTenantID(ctx, existingFast.ID, remoteID, slug)
	}

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
		if remote.UseCase == "pharmacy" {
			s.ensurePharmacyInventoryDefaults(ctx, realID)
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

	if remote.UseCase == "pharmacy" {
		s.ensurePharmacyInventoryDefaults(ctx, created.ID)
	}

	log.Printf("  [tenant-sync] dynamically created %s (UUID %s, synced from auth-api)", slug, created.ID)
	return created.ID, nil
}

// dueForDriftScan reports whether this slug's drift probe window has elapsed, stamping the
// slug as scanned when it has. Claiming the slot before the probe runs means concurrent
// requests for the same tenant make at most one auth-api call per interval.
func (s *Syncer) dueForDriftScan(slug string) bool {
	s.driftMu.Lock()
	defer s.driftMu.Unlock()
	if s.lastDriftScan == nil {
		s.lastDriftScan = make(map[string]time.Time)
	}
	if last, ok := s.lastDriftScan[slug]; ok && time.Since(last) < driftCheckInterval {
		return false
	}
	s.lastDriftScan[slug] = time.Now()
	return true
}

// probeAuthTenantID asks auth-api which UUID currently owns this slug. Errors are returned
// as-is so callers can distinguish "auth says something different" from "auth is down".
func (s *Syncer) probeAuthTenantID(ctx context.Context, endpoint string) (uuid.UUID, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return uuid.Nil, err
	}
	resp, err := driftProbeClient.Do(req)
	if err != nil {
		return uuid.Nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, fmt.Errorf("auth-api HTTP %d", resp.StatusCode)
	}
	var remote authAPITenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&remote); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(remote.ID)
}

// sqlLiteral renders s as a single-quoted Postgres string literal, doubling embedded quotes.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// adoptTenantIDSQL re-keys every tenant-scoped row from oldID onto newID in one transaction.
//
// Columns are discovered from the live catalog rather than hard-coded, using two sources:
// any column literally named tenant_id (most projections carry no FK at all), plus any column
// with a real FK to tenants(id) — Ent names one-to-one edge columns after the edge, not the
// entity, so a name-only sweep misses those and the final DELETE then fails on the FK.
//
// The clone lands on a throwaway slug so the unique(slug) index does not fire mid-transaction;
// the real slug is restored once the stale row is gone. Where a unique index makes a rewrite
// impossible (a row already exists under the new UUID with the same key — per-tenant sync
// bookkeeping), the stale duplicate is dropped instead.
const adoptTenantIDSQL = `
DO $$
DECLARE
  old_id text := %s;
  new_id text := %s;
  slug_v text := %s;
  r record;
  cols text;
  vals text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM tenants WHERE id::text = old_id) THEN RETURN; END IF;

  IF NOT EXISTS (SELECT 1 FROM tenants WHERE id::text = new_id) THEN
    SELECT string_agg(quote_ident(column_name), ', ' ORDER BY ordinal_position) INTO cols
      FROM information_schema.columns
     WHERE table_schema='public' AND table_name='tenants';
    SELECT string_agg(CASE WHEN column_name='id'   THEN quote_literal(new_id)
                           WHEN column_name='slug' THEN quote_literal('__drift_migrating__')
                           ELSE quote_ident(column_name) END, ', ' ORDER BY ordinal_position) INTO vals
      FROM information_schema.columns
     WHERE table_schema='public' AND table_name='tenants';
    EXECUTE format('INSERT INTO tenants (%%s) SELECT %%s FROM tenants WHERE id::text = %%L', cols, vals, old_id);
  END IF;

  FOR r IN
    SELECT c.table_name AS tbl, c.column_name AS col
      FROM information_schema.columns c
      JOIN information_schema.tables t
        ON t.table_schema=c.table_schema AND t.table_name=c.table_name AND t.table_type='BASE TABLE'
     WHERE c.table_schema='public' AND c.column_name='tenant_id'
    UNION
    SELECT con.conrelid::regclass::text, a.attname
      FROM pg_constraint con
      JOIN unnest(con.conkey) k ON true
      JOIN pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k
     WHERE con.confrelid='tenants'::regclass AND con.contype='f'
  LOOP
    BEGIN
      EXECUTE format('UPDATE %%I SET %%I = %%L WHERE %%I = %%L', r.tbl, r.col, new_id, r.col, old_id);
    EXCEPTION WHEN unique_violation OR foreign_key_violation THEN
      EXECUTE format('DELETE FROM %%I WHERE %%I = %%L', r.tbl, r.col, old_id);
      RAISE WARNING 'tenant drift: dropped stale rows from %% (collided under new UUID)', r.tbl;
    END;
  END LOOP;

  EXECUTE format('DELETE FROM tenants WHERE id::text = %%L', old_id);
  EXECUTE format('UPDATE tenants SET slug = %%L WHERE id::text = %%L', slug_v, new_id);
END $$;`

// adoptAuthTenantID re-keys the locally projected tenant onto the UUID auth-api reports,
// then returns that UUID. When no raw DB handle is wired up it degrades to a loud warning
// and keeps returning the local UUID — a partial rewrite would be worse than none.
func (s *Syncer) adoptAuthTenantID(ctx context.Context, localID, remoteID uuid.UUID, slug string) (uuid.UUID, error) {
	if s.db == nil {
		log.Printf("  [tenant-sync] cannot self-heal drift for %s: no DB handle wired (still using local %s)", slug, localID)
		return localID, nil
	}

	// A DO block takes no bind parameters, so the three values are inlined as quoted SQL
	// literals. localID/remoteID are already-parsed UUIDs; slug is quoted defensively.
	stmt := fmt.Sprintf(adoptTenantIDSQL,
		sqlLiteral(localID.String()), sqlLiteral(remoteID.String()), sqlLiteral(slug))
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		log.Printf("  [tenant-sync] self-heal for %s FAILED (still using local %s): %v", slug, localID, err)
		return localID, nil
	}

	if err := s.client.Tenant.UpdateOneID(remoteID).
		SetSyncStatus("synced").
		SetLastSyncAt(time.Now()).
		Exec(ctx); err != nil {
		log.Printf("  [tenant-sync] post-heal metadata refresh for %s failed: %v", slug, err)
	}

	log.Printf("  [tenant-sync] self-healed %s: %s -> %s", slug, localID, remoteID)
	return remoteID, nil
}

// ensurePharmacyInventoryDefaults flips a pharmacy-use-case tenant's costing method to FEFO
// (first-expire-first-out) and enables lot tracking — the batch-allocation behavior the DAWA
// workflow requires. Best-effort/idempotent: only touches a config row that's still on the
// factory defaults (wavg/no-lot-tracking), so it never clobbers an admin's later, deliberate
// override back to something else.
func (s *Syncer) ensurePharmacyInventoryDefaults(ctx context.Context, tenantID uuid.UUID) {
	cfg, err := s.client.TenantInventoryConfig.Query().
		Where(entconfig.TenantID(tenantID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		if _, createErr := s.client.TenantInventoryConfig.Create().
			SetTenantID(tenantID).
			SetCostingMethod(entconfig.CostingMethodFefo).
			SetEnableLotTracking(true).
			Save(ctx); createErr != nil {
			log.Printf("  [WARN] pharmacy inventory defaults: create config for %s: %v", tenantID, createErr)
		}
		return
	}
	if err != nil {
		log.Printf("  [WARN] pharmacy inventory defaults: query config for %s: %v", tenantID, err)
		return
	}
	if cfg.CostingMethod == entconfig.CostingMethodWavg && !cfg.EnableLotTracking {
		if _, updErr := cfg.Update().
			SetCostingMethod(entconfig.CostingMethodFefo).
			SetEnableLotTracking(true).
			Save(ctx); updErr != nil {
			log.Printf("  [WARN] pharmacy inventory defaults: update config for %s: %v", tenantID, updErr)
		}
	}
}
