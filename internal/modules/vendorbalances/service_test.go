package vendorbalances

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
	"github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/ent/vendorbalancecache"
)

// TestLookup_MatchesByVendorIDThenName pins the supplier → owed-balance join used by every
// supplier list/picker: a vendor_id-keyed cache row wins, an identifier-only row (treasury never
// resolved the inventory UUID) falls back to a case-insensitive name match, and a supplier with
// no AP record is left out rather than shown as a fabricated zero. Upsert is exercised too, since
// both the event consumer and the treasury resync write through it.
//
// Requires a real local Postgres via POSTGRES_URL (same convention as items/outlet_scope_test.go);
// skipped when unset.
func TestLookup_MatchesByVendorIDThenName(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed vendor balance lookup test")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() {
		_, _ = client.VendorBalanceCache.Delete().Where(vendorbalancecache.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Supplier.Delete().Where(supplier.TenantID(tenantID)).Exec(ctx)
	})

	mk := func(name string) *ent.Supplier {
		s, err := client.Supplier.Create().SetTenantID(tenantID).SetName(name).SetCode(name).Save(ctx)
		require.NoError(t, err)
		return s
	}
	byID, byName, none := mk("ELECTROMATE"), mk("Ahmed Saney"), mk("No AP Yet")

	require.NoError(t, Upsert(ctx, client, tenantID, Entry{VendorID: &byID.ID, VendorName: "ELECTROMATE", BalanceOwed: "1000", OutstandingPayable: "1000"}))
	// A second write for the same vendor_id updates in place rather than duplicating.
	require.NoError(t, Upsert(ctx, client, tenantID, Entry{VendorID: &byID.ID, VendorName: "ELECTROMATE", BalanceOwed: "4500.50", OutstandingPayable: "4500.50"}))
	require.NoError(t, Upsert(ctx, client, tenantID, Entry{VendorIdentifier: "legacy-ahmed", VendorName: "AHMED SANEY", BalanceOwed: "-200", OutstandingPayable: "0"}))

	n, err := client.VendorBalanceCache.Query().Where(vendorbalancecache.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	svc := NewService(client, nil, zap.NewNop())
	got := svc.Lookup(ctx, tenantID, []*ent.Supplier{byID, byName, none})

	require.Equal(t, "4500.50", got[byID.ID].BalanceOwed)
	require.Equal(t, "KES", got[byID.ID].Currency)
	require.Equal(t, "-200", got[byName.ID].BalanceOwed)
	_, ok := got[none.ID]
	require.False(t, ok, "a supplier with no AP record must be absent, not shown as zero")
}
