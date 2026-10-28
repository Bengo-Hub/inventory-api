package stock

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// TestResolveItemBySKUPrefersItem: an Item SKU always beats a legacy ItemVariant with the same
// SKU, and a variant SKU shared by two parents resolves deterministically instead of erroring.
func TestResolveItemBySKUPrefersItem(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_resolvesku_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop())

	tenantID := uuid.New()
	_, err := client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-" + tenantID.String()[:8]).Save(ctx)
	require.NoError(t, err)
	mk := func(sku string) *ent.Item {
		return client.Item.Create().SetTenantID(tenantID).SetSku(sku).SetName(sku).SetType(item.TypeGOODS).SaveX(ctx)
	}
	shirt, jacket, child := mk("SHIRT"), mk("JACKET"), mk("SHIRT-RED-M")

	// A legacy variant row carrying the same SKU as the child item.
	client.ItemVariant.Create().SetItemID(shirt.ID).SetSku("SHIRT-RED-M").SetName("Red M").SetPrice(10).SaveX(ctx)
	got, err := svc.ResolveItemBySKU(ctx, tenantID, "SHIRT-RED-M")
	require.NoError(t, err)
	require.Equal(t, child.ID, got.ID)
	require.Equal(t, "SHIRT-RED-M", svc.resolveStockSKU(ctx, tenantID, "SHIRT-RED-M"))

	// The same legacy variant SKU under two parents: the oldest row wins, no error.
	client.ItemVariant.Create().SetItemID(shirt.ID).SetSku("LEGACY-1").SetName("A").SetPrice(10).SaveX(ctx)
	client.ItemVariant.Create().SetItemID(jacket.ID).SetSku("LEGACY-1").SetName("B").SetPrice(10).SaveX(ctx)
	got, err = svc.ResolveItemBySKU(ctx, tenantID, "LEGACY-1")
	require.NoError(t, err)
	require.Equal(t, shirt.ID, got.ID)
	require.Equal(t, "SHIRT", svc.resolveStockSKU(ctx, tenantID, "LEGACY-1"))

	// Unknown SKUs stay not found.
	_, err = svc.ResolveItemBySKU(ctx, tenantID, "NOPE")
	require.True(t, ent.IsNotFound(err))
}
