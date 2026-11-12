package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/testutil"
)

// Auth renamed the tenant (same UUID, new slug). SyncTenant by the new slug reaches the
// update-by-id path, which must move the local row onto the new slug like create does.
func TestSyncTenantUpdatePathRenamesSlug(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_tenant_sync_slug_")
	ctx := context.Background()
	id := uuid.New()
	client.Tenant.Create().SetID(id).SetName("Old").SetSlug("old-co").ExecX(ctx)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id.String(), "slug": "new-co", "name": "New", "status": "active"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUTH_API_URL", srv.URL)

	got, err := NewSyncer(client, srv.URL).SyncTenant(ctx, "new-co")
	if err != nil || got != id {
		t.Fatalf("SyncTenant = %s, %v; want %s", got, err, id)
	}
	if row := client.Tenant.GetX(ctx, id); row.Slug != "new-co" || row.Name != "New" {
		t.Fatalf("row = slug %q name %q; want new-co / New", row.Slug, row.Name)
	}
}
