package items

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// TestListItemsReadReplicaRouting proves ListItems' multi-row branch actually uses the wired
// read client (not just that the field is set) and that the single-item (?id=) detail branch
// deliberately does NOT — see rc()'s doc comment and buildQuery's doc comment on the 2026-08-20
// read-routing change. Rather than standing up a real second Postgres instance, the "read"
// client is pointed at the database's own default `postgres` maintenance DB, which has no
// `items` table: if the list branch is wired correctly it fails with a table-not-found error
// (proving it hit that DB, not the real one); if the single-item branch were WRONGLY routed the
// same way it would fail identically — it must instead succeed against the primary.
func TestListItemsReadReplicaRouting(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed read-replica routing test")
	}

	openClient := func(t *testing.T, dsn string) *ent.Client {
		t.Helper()
		sqlDB, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, sqlDB.Ping())
		drv := entsql.OpenDB(dialect.Postgres, sqlDB)
		c := ent.NewClient(ent.Driver(drv))
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	primary := openClient(t, dsn)

	maintenanceDSN, err := swapDBName(dsn, "postgres")
	require.NoError(t, err)
	fakeReadReplica := openClient(t, maintenanceDSN)

	ctx := context.Background()
	svc := NewService(primary, zap.NewNop(), "")
	svc.SetReadClient(fakeReadReplica)

	tenantID := uuid.New()
	t.Cleanup(func() {
		_, _ = primary.Item.Delete().Where(item.TenantIDEQ(tenantID)).Exec(ctx)
		_ = primary.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = primary.Tenant.Create().SetID(tenantID).SetName("ReadRoutingTest").SetSlug("read-routing-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)
	seeded, err := primary.Item.Create().SetTenantID(tenantID).SetSku("SKU-RR-" + uuid.NewString()[:8]).SetName("Routing Probe").Save(ctx)
	require.NoError(t, err)

	t.Run("list branch hits the wired read client, not the primary", func(t *testing.T) {
		_, _, err := svc.ListItems(ctx, tenantID, "", "all", 10, 0, nil, nil, "", nil, nil, "")
		require.Error(t, err, "expected an error from the fake read-replica DB, which has no items table")
		require.Contains(t, strings.ToLower(err.Error()), "items", "error should come from a failed query against the (nonexistent-here) items table")
	})

	t.Run("single-item (?id=) branch stays on the primary regardless of the read client", func(t *testing.T) {
		idCtx := WithItemIDFilter(ctx, seeded.ID)
		dtos, total, err := svc.ListItems(idCtx, tenantID, "", "all", 10, 0, nil, nil, "", nil, nil, "")
		require.NoError(t, err, "the single-item detail path must succeed against the primary even though a (broken) read client is wired")
		require.Equal(t, 1, total)
		require.Len(t, dtos, 1)
		require.Equal(t, seeded.ID, dtos[0].ID)
	})
}

// swapDBName replaces the database name in a Postgres DSN, keeping every other component
// (host, credentials, query params) intact.
func swapDBName(dsn, newDBName string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + newDBName
	return u.String(), nil
}
