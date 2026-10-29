package items

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/testutil"
)

func TestSetDescriptionBySKUChangesOnlyTheDescription(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_desc_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID := uuid.New()
	_, err := client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-" + tenantID.String()[:8]).Save(ctx)
	require.NoError(t, err)
	created, err := svc.CreateItem(ctx, tenantID, ItemDTO{
		SKU: "KETTLE", Name: "Electric kettle", Type: "GOODS", IsActive: true,
		Tags: []string{"kitchen"}, Metadata: map[string]any{"note": "keep me"},
	})
	require.NoError(t, err)

	out, changed, err := svc.SetDescriptionBySKU(ctx, tenantID, "KETTLE", "  A kettle for the kitchen.  ")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "A kettle for the kitchen.", out.Description)

	itm, err := client.Item.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "Electric kettle", itm.Name)
	require.Equal(t, []string{"kitchen"}, itm.Tags)
	require.Equal(t, "keep me", itm.Metadata["note"])
	require.True(t, itm.IsActive)

	_, changed, err = svc.SetDescriptionBySKU(ctx, tenantID, "KETTLE", "A kettle for the kitchen.")
	require.NoError(t, err)
	require.False(t, changed, "same text is a no-op")

	_, _, err = svc.SetDescriptionBySKU(ctx, tenantID, "KETTLE", strings.Repeat("x", MaxDescriptionLength+1))
	require.ErrorIs(t, err, ErrDescriptionTooLong)

	_, _, err = svc.SetDescriptionBySKU(ctx, tenantID, "NOPE", "x")
	require.Error(t, err)
}
