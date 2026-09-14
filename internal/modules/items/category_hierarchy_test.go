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
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
)

// TestListCategoriesFilteredAncestorPropagation pins down the 2026-09-14 fix: a group/parent
// category with zero DIRECT items must still survive ListCategoriesFiltered(hasItems=true) when
// a descendant has items — real catalogs almost always link items to leaf categories only, never
// the group parent itself. Before the fix, the parent (and any ancestor above it) silently
// dropped out of the result, which orphaned every child's parentId and made storefront category
// trees collapse into a flat list (every child looked like a root).
//
// Requires a real local Postgres reachable via POSTGRES_URL (see outlet_scope_test.go for the
// same pattern); skipped when unset.
func TestListCategoriesFilteredAncestorPropagation(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed category-hierarchy regression test")
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
		_, _ = client.Item.Delete().Where(item.TenantIDEQ(tenantID)).Exec(ctx)
		_, _ = client.ItemCategory.Delete().Where(itemcategory.TenantIDEQ(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Category Hierarchy Test Tenant").
		SetSlug("category-hierarchy-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)

	// root -> parent -> leaf; only the leaf gets a real item linked to it.
	root, err := svc.CreateCategory(ctx, tenantID, CategoryDTO{Name: "Home & Living"})
	require.NoError(t, err)
	parent, err := svc.CreateCategory(ctx, tenantID, CategoryDTO{Name: "Kitchen & Dining", ParentID: &root.ID})
	require.NoError(t, err)
	leaf, err := svc.CreateCategory(ctx, tenantID, CategoryDTO{Name: "Cookware", ParentID: &parent.ID})
	require.NoError(t, err)

	_, err = client.Item.Create().
		SetTenantID(tenantID).
		SetSku("COOKWARE-TEST-" + uuid.NewString()[:8]).
		SetName("Non-stick Pan").
		SetCategoryID(leaf.ID).
		SetIsActive(true).
		Save(ctx)
	require.NoError(t, err)

	cats, err := svc.ListCategoriesFiltered(ctx, tenantID, true, false)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(cats))
	for _, c := range cats {
		ids[c.ID] = true
	}
	require.True(t, ids[leaf.ID], "leaf category (has a direct item) must survive the filter")
	require.True(t, ids[parent.ID], "parent category (item only on its child) must survive via ancestor propagation")
	require.True(t, ids[root.ID], "root category (item only on its grandchild) must survive via ancestor propagation")
}
