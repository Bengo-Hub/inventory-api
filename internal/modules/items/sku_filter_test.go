package items

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

func TestWithSKUFilterCleans(t *testing.T) {
	ctx := WithSKUFilter(context.Background(), []string{" A ", "", "B", "A", "  "})
	require.Equal(t, []string{"A", "B"}, skuFilter(ctx))

	require.Nil(t, skuFilter(WithSKUFilter(context.Background(), []string{"", " "})), "an empty list sets no filter")

	many := make([]string, MaxSKUFilter+10)
	for i := range many {
		many[i] = fmt.Sprintf("S%d", i)
	}
	require.Len(t, skuFilter(WithSKUFilter(context.Background(), many)), MaxSKUFilter)
}

// TestListItemsSKUFilter: ?skus= returns exactly the named items of the tenant, enriched like any
// list row. Runs against a real Postgres (POSTGRES_URL), skipped when unset like the other list tests.
func TestListItemsSKUFilter(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed SKU filter test")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)
	tenantID, otherTenant := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = client.Item.Delete().Where(item.TenantIDIn(tenantID, otherTenant)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
		_ = client.Tenant.DeleteOneID(otherTenant).Exec(ctx)
	})
	for _, id := range []uuid.UUID{tenantID, otherTenant} {
		_, err = client.Tenant.Create().SetID(id).SetName("SKU Filter").SetSlug("sku-filter-" + id.String()).Save(ctx)
		require.NoError(t, err)
	}
	suffix := uuid.NewString()[:8]
	mk := func(tid uuid.UUID, sku string) {
		_, err := client.Item.Create().SetTenantID(tid).SetSku(sku).SetName(sku).SetType(item.TypeGOODS).Save(ctx)
		require.NoError(t, err)
	}
	a, b, c := "A-"+suffix, "B-"+suffix, "C-"+suffix
	mk(tenantID, a)
	mk(tenantID, b)
	mk(tenantID, c)
	mk(otherTenant, "X-"+suffix)

	rows, total, err := svc.ListItems(WithSKUFilter(ctx, []string{a, c, "X-" + suffix, "missing"}), tenantID, "", "all", 50, 0, nil, nil, "", nil, nil, "")
	require.NoError(t, err)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.SKU] = true
	}
	require.Equal(t, 2, total)
	require.Equal(t, map[string]bool{a: true, c: true}, got, "only the tenant's own named SKUs come back")
}
