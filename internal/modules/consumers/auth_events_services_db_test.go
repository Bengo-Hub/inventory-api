package consumers

import (
	"context"
	"testing"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	"github.com/bengobox/inventory-service/internal/ent/userroleassignment"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// auth-api's services list on an auth.user event decides whether inventory keeps the member: a
// list without inventory removes this tenant's copy (user, roles, outlets) on updated and pin_set
// alike; an event without a list never removes anybody.
func TestAuthEventServicesListRemovesLocalCopy(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_user_services_")
	ctx := context.Background()
	c := NewAuthEventsConsumer(zap.NewNop(), nil, client, nil)
	tenantID := uuid.New()
	now := time.Now()
	role := client.InventoryRole.Create().SetRoleCode("viewer").SetName("Viewer").SetIsSystemRole(true).
		SetCreatedAt(now).SetUpdatedAt(now).SaveX(ctx)
	seed := func() uuid.UUID {
		authID := uuid.New()
		u := client.InventoryUser.Create().SetTenantID(tenantID).SetAuthServiceUserID(authID).
			SetEmail(authID.String() + "@shop.test").SetStatus("active").SetSyncStatus("synced").
			SetCreatedAt(now).SetUpdatedAt(now).SaveX(ctx)
		client.UserRoleAssignment.Create().SetTenantID(tenantID).SetUserID(u.ID).SetRoleID(role.ID).
			SetAssignedBy(authID).SetAssignedAt(now).ExecX(ctx)
		client.UserOutlet.Create().SetTenantID(tenantID).SetUserID(authID).SetOutletID(uuid.New()).ExecX(ctx)
		return authID
	}
	event := func(authID uuid.UUID, kind string, payload map[string]any) *sharedevents.Event {
		payload["user_id"] = authID.String()
		payload["tenant_id"] = tenantID.String()
		return sharedevents.NewEvent(kind, "auth.user", authID, tenantID, payload)
	}
	present := func(authID uuid.UUID) bool {
		return client.InventoryUser.Query().Where(entinvuser.AuthServiceUserID(authID)).ExistX(ctx) ||
			client.UserOutlet.Query().Where(entuseroutlet.UserID(authID)).ExistX(ctx)
	}

	cashier := seed()
	if err := c.handleUserUpdated(ctx, event(cashier, "updated", map[string]any{"roles": []any{"cashier"}, "services": []any{"pos", "ordering"}})); err != nil {
		t.Fatal(err)
	}
	if present(cashier) {
		t.Fatal("a member whose services leave out inventory must be removed")
	}
	if client.UserRoleAssignment.Query().Where(userroleassignment.TenantID(tenantID)).CountX(ctx) != 0 {
		t.Fatal("the removed member's role assignment must go too")
	}

	terminal := seed()
	if err := c.handlePinSet(ctx, event(terminal, "pin_set", map[string]any{"service": "inventory", "pin_hash": "x", "services": []any{}})); err != nil {
		t.Fatal(err)
	}
	if present(terminal) {
		t.Fatal("an empty services list is a decided no, on pin_set too")
	}

	// Removing the member in another tenant leaves this tenant's copy alone.
	other := seed()
	if err := c.removeLocalUser(ctx, &[]uuid.UUID{uuid.New()}[0], other); err != nil || !present(other) {
		t.Fatalf("removal in another tenant touched this one: err %v", err)
	}
	// Without a list nothing is revoked (the role rules apply; this one is not even reached).
	if handled, err := c.revoked(ctx, event(other, "updated", map[string]any{"roles": []any{"cashier"}}), other); handled || err != nil {
		t.Fatalf("an event without services must not revoke: handled %v err %v", handled, err)
	}
	if !present(other) {
		t.Fatal("member removed by an event without a services list")
	}
}
