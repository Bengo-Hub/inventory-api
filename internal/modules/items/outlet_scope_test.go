package items

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// TestOutletScope exercises OutletScope against a real Postgres connection, targeting the exact
// 3 regression classes documented in the function's own comments (fresh-outlet leniency,
// cleared-but-experienced outlet, normal cross-outlet exclusion). Added alongside the 2026-08-20
// perf fix (a fast existence-check short-circuit for the "no balance rows here at all" case) to
// pin down that the short-circuit is byte-identical to the pre-existing full computation it
// replaces for that case, and that the unmodified "has history" path still behaves as documented.
//
// Requires a real local Postgres reachable via POSTGRES_URL (matches this repo's existing local
// dev workflow — see feedback_ent_atlas_migrations memory); skipped when unset, e.g. in CI
// environments without a DB configured (a known, pre-existing gap fleet-wide, not introduced
// here — see platform-admin-rbac-gating-fix-2026-08-18 memory).
func TestOutletScope(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed OutletScope regression test")
	}

	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), "")

	tenantID := uuid.New()
	t.Cleanup(func() {
		// Best-effort teardown scoped to this test's own tenant_id only, child rows first (FK order).
		_, _ = client.InventoryBalance.Delete().Where(inventorybalance.TenantIDEQ(tenantID)).Exec(ctx)
		_, _ = client.Item.Delete().Where(item.TenantIDEQ(tenantID)).Exec(ctx)
		_, _ = client.Warehouse.Delete().Where(warehouse.TenantID(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("OutletScope Test Tenant").SetSlug("outlet-scope-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)

	whA, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Outlet A WH").SetCode("A-" + uuid.NewString()[:8]).Save(ctx)
	require.NoError(t, err)
	whB, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Outlet B WH").SetCode("B-" + uuid.NewString()[:8]).Save(ctx)
	require.NoError(t, err)
	outletA := uuid.New()
	outletB := uuid.New()
	_, err = client.Warehouse.UpdateOne(whA).SetOutletID(outletA).Save(ctx)
	require.NoError(t, err)
	_, err = client.Warehouse.UpdateOne(whB).SetOutletID(outletB).Save(ctx)
	require.NoError(t, err)

	itemElsewhereOnly, err := client.Item.Create().SetTenantID(tenantID).SetSku("SKU-ELSEWHERE-" + uuid.NewString()[:8]).SetName("Elsewhere Only").Save(ctx)
	require.NoError(t, err)
	itemBoth, err := client.Item.Create().SetTenantID(tenantID).SetSku("SKU-BOTH-" + uuid.NewString()[:8]).SetName("Both Outlets").Save(ctx)
	require.NoError(t, err)
	itemRemovedFromA, err := client.Item.Create().SetTenantID(tenantID).SetSku("SKU-REMOVED-" + uuid.NewString()[:8]).SetName("Removed From A").Save(ctx)
	require.NoError(t, err)
	itemNeverReceived, err := client.Item.Create().SetTenantID(tenantID).SetSku("SKU-UNRECEIVED-" + uuid.NewString()[:8]).SetName("Never Received").Save(ctx)
	require.NoError(t, err)

	t.Run("fresh outlet with zero balance rows sees everything (fast-path short-circuit)", func(t *testing.T) {
		freshOutlet := uuid.New()
		whFresh, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Fresh WH").SetCode("F-" + uuid.NewString()[:8]).SetOutletID(freshOutlet).Save(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Warehouse.DeleteOne(whFresh).Exec(ctx) })

		itemStockedElsewhere, err := client.Item.Create().SetTenantID(tenantID).SetSku("SKU-FRESH-CASE-" + uuid.NewString()[:8]).SetName("Stocked At B Only").Save(ctx)
		require.NoError(t, err)
		// Some OTHER outlet (B) has stock — a fresh outlet must still see it (never hidden).
		_, err = client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemStockedElsewhere.ID).SetWarehouseID(whB.ID).Save(ctx)
		require.NoError(t, err)

		exclude, hasHistory, _, err := svc.OutletScope(ctx, tenantID, &freshOutlet)
		require.NoError(t, err)
		require.False(t, hasHistory, "a fresh outlet must report no operational history")
		require.Empty(t, exclude, "a fresh outlet must never hide any item, however other outlets are stocked")
	})

	t.Run("outlet with own stock excludes items stocked exclusively elsewhere", func(t *testing.T) {
		_, err := client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemElsewhereOnly.ID).SetWarehouseID(whB.ID).Save(ctx)
		require.NoError(t, err)
		_, err = client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemBoth.ID).SetWarehouseID(whA.ID).Save(ctx)
		require.NoError(t, err)
		_, err = client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemBoth.ID).SetWarehouseID(whB.ID).Save(ctx)
		require.NoError(t, err)

		exclude, hasHistory, _, err := svc.OutletScope(ctx, tenantID, &outletA)
		require.NoError(t, err)
		require.True(t, hasHistory)
		require.Contains(t, exclude, itemElsewhereOnly.ID, "item stocked exclusively at outlet B must be hidden from outlet A")
		require.NotContains(t, exclude, itemBoth.ID, "item stocked at BOTH outlets must remain visible at outlet A")
		require.NotContains(t, exclude, itemNeverReceived.ID, "an item with no balance row anywhere is never hidden by this rule alone")
	})

	t.Run("cleared-but-experienced outlet (removed_from_location) still excludes elsewhere-only items", func(t *testing.T) {
		// itemRemovedFromA had stock at A that was fully moved out (removed_from_location=true,
		// on_hand back to 0) — outletA has real operational history via this row even though
		// stockedHere for it is empty. This is the exact 2026-08-14 regression class.
		_, err := client.InventoryBalance.Create().
			SetTenantID(tenantID).SetItemID(itemRemovedFromA.ID).SetWarehouseID(whA.ID).
			SetRemovedFromLocation(true).Save(ctx)
		require.NoError(t, err)

		exclude, hasHistory, _, err := svc.OutletScope(ctx, tenantID, &outletA)
		require.NoError(t, err)
		require.True(t, hasHistory)
		require.Contains(t, exclude, itemRemovedFromA.ID, "an item explicitly removed from this outlet's own warehouse must be hidden")
		require.Contains(t, exclude, itemElsewhereOnly.ID, "cleared-but-experienced outlet must still hide items stocked only at other outlets")
	})
}
