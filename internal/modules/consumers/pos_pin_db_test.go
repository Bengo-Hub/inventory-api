package consumers

import (
	"context"
	"testing"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// A member newly given inventory keeps their till PIN, an existing inventory PIN is never replaced,
// and a POS PIN change follows only for someone already in inventory.
func TestPOSPinCarriesOver(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_pos_pin_")
	ctx := context.Background()
	c := NewAuthEventsConsumer(zap.NewNop(), nil, client, nil)
	tenantID := uuid.New()
	now := time.Now()
	calls := 0
	c.SetPOSPinLookup(func(context.Context, uuid.UUID, uuid.UUID) (string, error) { calls++; return "pos-hash", nil })
	seed := func(pin string) uuid.UUID {
		authID := uuid.New()
		q := client.InventoryUser.Create().SetTenantID(tenantID).SetAuthServiceUserID(authID).
			SetEmail(authID.String() + "@shop.test").SetStatus("active").SetSyncStatus("synced").
			SetCreatedAt(now).SetUpdatedAt(now)
		if pin != "" {
			q = q.SetPinHash(pin)
		}
		q.ExecX(ctx)
		return authID
	}
	pinOf := func(authID uuid.UUID) string {
		u := client.InventoryUser.Query().Where(entinvuser.AuthServiceUserID(authID)).OnlyX(ctx)
		if u.PinHash == nil {
			return ""
		}
		return *u.PinHash
	}

	fresh := seed("")
	c.adoptPOSPin(ctx, tenantID, fresh)
	if pinOf(fresh) != "pos-hash" {
		t.Fatalf("a member with no PIN must adopt their till PIN, got %q", pinOf(fresh))
	}
	calls = 0
	own := seed("own-hash")
	c.adoptPOSPin(ctx, tenantID, own)
	if pinOf(own) != "own-hash" || calls != 0 {
		t.Fatalf("an existing PIN must not be replaced or looked up: %q, %d calls", pinOf(own), calls)
	}

	// A POS PIN change follows for a member here, and creates nobody.
	event := func(authID uuid.UUID) *sharedevents.Event {
		return sharedevents.NewEvent("pin_set", "auth.user", authID, tenantID, map[string]any{
			"user_id": authID.String(), "service": "pos", "pin_hash": "new-hash",
		})
	}
	if err := c.handlePinSet(ctx, event(own)); err != nil {
		t.Fatal(err)
	}
	if pinOf(own) != "new-hash" {
		t.Fatalf("a POS PIN change must follow, got %q", pinOf(own))
	}
	outsider := uuid.New()
	if err := c.handlePinSet(ctx, event(outsider)); err != nil {
		t.Fatal(err)
	}
	if client.InventoryUser.Query().Where(entinvuser.AuthServiceUserID(outsider)).ExistX(ctx) {
		t.Fatal("a POS PIN must not create an inventory user")
	}
}
