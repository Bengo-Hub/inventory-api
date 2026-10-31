package items

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
)

func TestNormalizeKRAItemClass(t *testing.T) {
	for code, ok := range map[string]bool{"1000000000": true, " 50202306 ": true, "": true, "12345": false, "10000000001": false, "50A0230600": false} {
		if _, got := NormalizeKRAItemClass(code); got != ok {
			t.Errorf("NormalizeKRAItemClass(%q) = %v, want %v", code, got, ok)
		}
	}
}

func TestCategoryAncestorIDs(t *testing.T) {
	root, parent, self := uuid.New(), uuid.New(), uuid.New()
	got := categoryAncestorIDs(&ent.ItemCategory{ID: self, Path: root.String() + "/" + parent.String() + "/" + self.String()})
	require.Equal(t, []uuid.UUID{root, parent}, got, "ancestors root first, without the category itself")
	legacy := categoryAncestorIDs(&ent.ItemCategory{ID: self, ParentID: &parent})
	require.Equal(t, []uuid.UUID{parent}, legacy, "a row with no path falls back to its parent")
}

func TestItemKRAClass(t *testing.T) {
	own := "5020230602"
	code, src := itemKRAClass(&ent.Item{EtimsItemClsCd: &own}, "5020230600")
	require.Equal(t, own, code)
	require.Equal(t, KRAClassFromItem, src)
	code, src = itemKRAClass(&ent.Item{}, "5020230600")
	require.Equal(t, "5020230600", code)
	require.Equal(t, KRAClassFromCategory, src)
	code, src = itemKRAClass(&ent.Item{}, "")
	require.Empty(t, code)
	require.Empty(t, src)
}

// A category with no classification of its own inherits its nearest ancestor's; setting one on the
// category itself wins; clearing it falls back again. Requires POSTGRES_URL (local inventory DB).
func TestResolveCategoryKRAClassesInherits(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed KRA classification test")
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
		_, _ = client.ItemCategory.Delete().Where(itemcategory.TenantIDEQ(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("KRA class test").SetSlug("kra-class-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)

	wines := "5020230600"
	root, err := svc.CreateCategory(ctx, tenantID, CategoryDTO{Name: "Wines", KRAItemClsCd: &wines})
	require.NoError(t, err)
	red, err := svc.CreateCategory(ctx, tenantID, CategoryDTO{Name: "Red Wines", ParentID: &root.ID})
	require.NoError(t, err)
	require.Nil(t, red.KRAItemClsCd)

	redRow, err := client.ItemCategory.Get(ctx, red.ID)
	require.NoError(t, err)
	require.Equal(t, wines, svc.resolveCategoryKRAClasses(ctx, []*ent.ItemCategory{redRow})[red.ID], "a child inherits its parent's class")

	_, err = svc.SetCategoryKRAClass(ctx, tenantID, red.ID, "5020230602")
	require.NoError(t, err)
	require.Equal(t, "5020230602", svc.categoryKRAClass(ctx, &red.ID), "the category's own class wins")

	_, err = svc.SetCategoryKRAClass(ctx, tenantID, red.ID, "")
	require.NoError(t, err)
	require.Equal(t, wines, svc.categoryKRAClass(ctx, &red.ID), "clearing falls back to the parent")

	_, err = svc.SetCategoryKRAClass(ctx, tenantID, red.ID, "12")
	require.ErrorIs(t, err, ErrInvalidKRAItemClass)
	_, err = svc.SetCategoryKRAClass(ctx, uuid.New(), red.ID, wines)
	require.ErrorIs(t, err, ErrCategoryNotFound, "another tenant cannot set it")
}
