package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/testutil"
)

// Plan caps count only active rows: a deactivated warehouse or an archived supplier must not use
// up a slot, and another tenant's rows never count.
func TestPlanLimitCountsOnlyActiveRows(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_lim_")
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tenant, other} {
		client.Tenant.Create().SetID(id).SetName("Limit Test").SetSlug("lim-" + id.String()).SaveX(ctx)
	}

	client.Warehouse.Create().SetTenantID(tenant).SetName("Main").SetCode("MAIN").SaveX(ctx)
	client.Warehouse.Create().SetTenantID(tenant).SetName("Old").SetCode("OLD").SetIsActive(false).SaveX(ctx)
	client.Warehouse.Create().SetTenantID(other).SetName("Elsewhere").SetCode("ELS").SaveX(ctx)

	client.Supplier.Create().SetTenantID(tenant).SetName("Live").SetCode("LIVE").SaveX(ctx)
	client.Supplier.Create().SetTenantID(tenant).SetName("Archived").SetCode("ARCH").SetIsActive(false).SaveX(ctx)
	client.Supplier.Create().SetTenantID(other).SetName("Theirs").SetCode("THEIRS").SaveX(ctx)

	if n, err := countActiveWarehouses(ctx, client, tenant); err != nil || n != 1 {
		t.Fatalf("active warehouses = %d, %v; want 1", n, err)
	}
	if n, err := countActiveSuppliers(ctx, client, tenant); err != nil || n != 1 {
		t.Fatalf("active suppliers = %d, %v; want 1", n, err)
	}
}
