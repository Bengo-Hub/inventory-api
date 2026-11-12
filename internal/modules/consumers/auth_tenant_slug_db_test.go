package consumers

import (
	"context"
	"testing"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/testutil"
)

// auth.tenant.updated after a rename (same UUID, new slug) moves the local tenants row onto the
// new slug; a slug another local row already holds is left alone.
func TestTenantUpdatedRenamesLocalSlug(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_tenant_slug_")
	ctx := context.Background()
	c := NewAuthEventsConsumer(zap.NewNop(), nil, client, nil)

	id, other := uuid.New(), uuid.New()
	client.Tenant.Create().SetID(id).SetName("Co").SetSlug("old-co").ExecX(ctx)
	client.Tenant.Create().SetID(other).SetName("Taken").SetSlug("taken-co").ExecX(ctx)

	evt := func(slug string) *sharedevents.Event {
		return sharedevents.NewEvent("updated", "auth.tenant", id, id, map[string]any{"tenant_id": id.String(), "slug": slug})
	}
	if err := c.handleTenantChanged(ctx, evt("new-co")); err != nil {
		t.Fatal(err)
	}
	if got := client.Tenant.GetX(ctx, id).Slug; got != "new-co" {
		t.Fatalf("slug = %q, want new-co", got)
	}
	if err := c.handleTenantChanged(ctx, evt("taken-co")); err != nil {
		t.Fatal(err)
	}
	if got := client.Tenant.GetX(ctx, id).Slug; got != "new-co" {
		t.Fatalf("a clashing slug must not move the row, got %q", got)
	}
}
