package stockcalc

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// TestOutletWarehouse pins where stock for an outlet lands: the outlet's own warehouse, else the
// tenant default (no outlet, an outlet without a warehouse, or an outlet from another tenant).
// 2026-11-11: an item created at Demo City Supermarket put its opening stock in the hotel's
// default warehouse, so it never showed on the supermarket's catalogue, till or eTIMS Items page.
// Requires POSTGRES_URL; skipped otherwise, like the other DB-backed tests in this repo.
func TestOutletWarehouse(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed OutletWarehouse test")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	tenantID, otherTenant := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = client.Warehouse.Delete().Where(warehouse.TenantIDIn(tenantID, otherTenant)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
		_ = client.Tenant.DeleteOneID(otherTenant).Exec(ctx)
	})
	for _, id := range []uuid.UUID{tenantID, otherTenant} {
		_, err = client.Tenant.Create().SetID(id).SetName("OutletWarehouse test").SetSlug("outlet-wh-test-" + id.String()).Save(ctx)
		require.NoError(t, err)
	}

	hqOutlet, branchOutlet, bareOutlet, foreignOutlet := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	hq, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("HQ").SetCode("HQ-" + uuid.NewString()[:8]).
		SetIsDefault(true).SetOutletID(hqOutlet).Save(ctx)
	require.NoError(t, err)
	branch, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Branch").SetCode("BR-" + uuid.NewString()[:8]).
		SetOutletID(branchOutlet).Save(ctx)
	require.NoError(t, err)
	_, err = client.Warehouse.Create().SetTenantID(otherTenant).SetName("Foreign").SetCode("FX-" + uuid.NewString()[:8]).
		SetOutletID(foreignOutlet).Save(ctx)
	require.NoError(t, err)

	cases := map[string]struct {
		outlet uuid.UUID
		want   uuid.UUID
	}{
		"branch outlet gets its own warehouse":     {branchOutlet, branch.ID},
		"no outlet gets the tenant default":        {uuid.Nil, hq.ID},
		"outlet without a warehouse gets default":  {bareOutlet, hq.ID},
		"another tenant's outlet gets our default": {foreignOutlet, hq.ID},
	}
	for name, c := range cases {
		got, err := OutletWarehouse(ctx, client, tenantID, c.outlet)
		require.NoError(t, err, name)
		require.Equal(t, c.want, got.ID, name)
	}

	// A tenant with no default and no outlet warehouse is a NotFound, not a panic.
	_, err = OutletWarehouse(ctx, client, uuid.New(), uuid.Nil)
	require.True(t, ent.IsNotFound(err))
}
