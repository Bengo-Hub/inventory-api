package items

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
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// TestStrictOutletPredicate pins the strict outlet catalog used by ?outlet_strict=1: stock-tracked
// items need an active balance at the outlet's own or a shared warehouse, and made-to-order items
// must sit in a category relevant to the outlet's use_case. Runs against a real Postgres
// (POSTGRES_URL), skipped when unset like TestOutletScope.
func TestStrictOutletPredicate(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed strict outlet scope test")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)
	tenantID := uuid.New()
	t.Cleanup(func() {
		_, _ = client.InventoryBalance.Delete().Where(inventorybalance.TenantIDEQ(tenantID)).Exec(ctx)
		_, _ = client.Item.Delete().Where(item.TenantIDEQ(tenantID)).Exec(ctx)
		_, _ = client.ItemCategory.Delete().Where(itemcategory.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Warehouse.Delete().Where(warehouse.TenantID(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Strict Scope Tenant").SetSlug("strict-scope-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)

	outletA, outletB := uuid.New(), uuid.New()
	whA, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("A").SetCode("A-" + uuid.NewString()[:8]).SetOutletID(outletA).SetUseCase("retail").Save(ctx)
	require.NoError(t, err)
	whB, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("B").SetCode("B-" + uuid.NewString()[:8]).SetOutletID(outletB).SetUseCase("hospitality").Save(ctx)
	require.NoError(t, err)

	retailCat, err := client.ItemCategory.Create().SetTenantID(tenantID).SetName("Retail menu").SetUseCases([]string{"retail"}).Save(ctx)
	require.NoError(t, err)
	hospCat, err := client.ItemCategory.Create().SetTenantID(tenantID).SetName("Hot Beverages").SetUseCases([]string{"hospitality"}).Save(ctx)
	require.NoError(t, err)
	universalCat, err := client.ItemCategory.Create().SetTenantID(tenantID).SetName("Universal").Save(ctx)
	require.NoError(t, err)

	mk := func(name string, typ item.Type, cat *ent.ItemCategory) *ent.Item {
		c := client.Item.Create().SetTenantID(tenantID).SetSku(name + "-" + uuid.NewString()[:8]).SetName(name).SetType(typ)
		if cat != nil {
			c = c.SetCategoryID(cat.ID)
		}
		it, err := c.Save(ctx)
		require.NoError(t, err)
		return it
	}
	goodsHere := mk("goods-here", item.TypeGOODS, nil)
	goodsElsewhere := mk("goods-elsewhere", item.TypeGOODS, nil)
	goodsRemoved := mk("goods-removed", item.TypeGOODS, nil)
	goodsUnreceived := mk("goods-unreceived", item.TypeGOODS, nil)
	serviceRetail := mk("service-retail", item.TypeSERVICE, retailCat)
	serviceHosp := mk("service-hospitality", item.TypeSERVICE, hospCat)
	serviceUniversal := mk("service-universal", item.TypeSERVICE, universalCat)
	serviceNoCat := mk("service-nocat", item.TypeSERVICE, nil)

	bal := func(it *ent.Item, wh *ent.Warehouse, removed bool) {
		_, err := client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(it.ID).SetWarehouseID(wh.ID).SetRemovedFromLocation(removed).Save(ctx)
		require.NoError(t, err)
	}
	bal(goodsHere, whA, false)
	bal(goodsElsewhere, whB, false)
	bal(goodsRemoved, whA, true)

	scope := map[uuid.UUID]struct{}{whA.ID: {}}
	pred, err := svc.strictOutletPredicate(ctx, tenantID, outletA, scope)
	require.NoError(t, err)
	got, err := client.Item.Query().Where(item.TenantIDEQ(tenantID), pred).IDs(ctx)
	require.NoError(t, err)

	require.ElementsMatch(t, []uuid.UUID{goodsHere.ID, serviceRetail.ID, serviceUniversal.ID, serviceNoCat.ID}, got)
	for _, hidden := range []*ent.Item{goodsElsewhere, goodsRemoved, goodsUnreceived, serviceHosp} {
		require.NotContains(t, got, hidden.ID, hidden.Name)
	}
}
