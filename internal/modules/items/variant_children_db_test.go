package items

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/testutil"
)

func f64(v float64) *float64 { return &v }

// TestSaveVariantsMatrix covers the variant child item lifecycle: a 2x2 matrix creates four
// children named, priced and SKU'd from the parent; a resave without one row deactivates it; a
// repeated combination is rejected; the catalog listing shows the parent with its variants and
// hides the children, while a plain listing and a search still reach them.
func TestSaveVariantsMatrix(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_variants_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID := uuid.New()
	_, err := client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-" + tenantID.String()[:8]).Save(ctx)
	require.NoError(t, err)
	client.Warehouse.Create().SetTenantID(tenantID).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)

	parent, err := svc.CreateItem(ctx, tenantID, ItemDTO{
		SKU: "TSHIRT", Name: "T-shirt", Type: "GOODS", IsActive: true,
		TaxCodeID: "VAT16", EtimsItemClsCd: "5310000000", MaxSellingPrice: f64(1000),
		Metadata: map[string]any{"note": "keep me"},
	})
	require.NoError(t, err)

	in := SaveVariantsInput{
		Axes: map[string][]string{"size": {"S", "M"}, "Colour": {"Red", "Blue"}},
		Rows: []VariantRowInput{
			{Attributes: map[string]string{"size": "S", "colour": "Red"}, Price: f64(1200)},
			{Attributes: map[string]string{"size": "M", "colour": "red"}},
			{Attributes: map[string]string{"size": "S", "colour": "Blue"}, SKU: "TS-BLUE-S", Barcode: "6001000000011"},
			{Attributes: map[string]string{"size": "M", "colour": "Blue"}},
		},
	}
	out, err := svc.SaveVariants(ctx, tenantID, parent.ID, in)
	require.NoError(t, err)
	require.Len(t, out, 4)

	bySKU := map[string]VariantDTO{}
	for _, v := range out {
		bySKU[v.SKU] = v
		require.True(t, v.IsActive)
	}
	require.Contains(t, bySKU, "TSHIRT-RED-S")
	require.Contains(t, bySKU, "TSHIRT-RED-M")
	require.Contains(t, bySKU, "TS-BLUE-S")
	require.Contains(t, bySKU, "TSHIRT-BLUE-M")
	require.Equal(t, "T-shirt, Red / S", bySKU["TSHIRT-RED-S"].Name)
	require.Equal(t, "T-shirt, Red / M", bySKU["TSHIRT-RED-M"].Name)
	require.Equal(t, map[string]string{"colour": "Red", "size": "M"}, bySKU["TSHIRT-RED-M"].Attributes)
	require.Equal(t, 1200.0, bySKU["TSHIRT-RED-S"].Price)
	require.Equal(t, 1000.0, bySKU["TSHIRT-RED-M"].Price) // inherits the parent's price
	require.Equal(t, "6001000000011", bySKU["TS-BLUE-S"].Barcode)
	// Axis order: colour (Red, Blue) then size (S, M).
	require.Equal(t, []string{"TSHIRT-RED-S", "TSHIRT-RED-M", "TS-BLUE-S", "TSHIRT-BLUE-M"},
		[]string{out[0].SKU, out[1].SKU, out[2].SKU, out[3].SKU})

	child, err := client.Item.Get(ctx, bySKU["TSHIRT-RED-S"].ID)
	require.NoError(t, err)
	require.Equal(t, "VAT16", child.TaxCodeID)
	require.Equal(t, "5310000000", derefStr(child.EtimsItemClsCd))
	require.Equal(t, parent.ID.String(), child.Metadata[MetaVariantOf])

	stored, err := client.Item.Get(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, "keep me", stored.Metadata["note"])
	require.Equal(t, map[string][]string{"colour": {"Red", "Blue"}, "size": {"S", "M"}}, VariantAxesFromMetadata(stored.Metadata))

	// Saving the same matrix again is a no-op: no duplicates.
	again, err := svc.SaveVariants(ctx, tenantID, parent.ID, in)
	require.NoError(t, err)
	require.Len(t, again, 4)

	// Resave without Blue / M: that child is deactivated, not deleted.
	in.Rows = in.Rows[:3]
	out, err = svc.SaveVariants(ctx, tenantID, parent.ID, in)
	require.NoError(t, err)
	require.Len(t, out, 4)
	gone, err := client.Item.Get(ctx, bySKU["TSHIRT-BLUE-M"].ID)
	require.NoError(t, err)
	require.False(t, gone.IsActive)

	// A repeated combination is rejected.
	_, err = svc.SaveVariants(ctx, tenantID, parent.ID, SaveVariantsInput{
		Axes: in.Axes,
		Rows: []VariantRowInput{
			{Attributes: map[string]string{"size": "S", "colour": "Red"}},
			{Attributes: map[string]string{"size": "s", "colour": "RED"}},
		},
	})
	var inErr *VariantInputError
	require.True(t, errors.As(err, &inErr), "got %v", err)

	// A value outside the axes is rejected.
	_, err = svc.SaveVariants(ctx, tenantID, parent.ID, SaveVariantsInput{
		Axes: in.Axes,
		Rows: []VariantRowInput{{Attributes: map[string]string{"size": "XL", "colour": "Red"}}},
	})
	require.True(t, errors.As(err, &inErr), "got %v", err)

	// A SKU held by another item is a conflict.
	_, err = svc.CreateItem(ctx, tenantID, ItemDTO{SKU: "TAKEN", Name: "Other", Type: "GOODS", IsActive: true})
	require.NoError(t, err)
	_, err = svc.SaveVariants(ctx, tenantID, parent.ID, SaveVariantsInput{
		Axes: in.Axes,
		Rows: []VariantRowInput{{Attributes: map[string]string{"size": "S", "colour": "Red"}, SKU: "TAKEN"}},
	})
	var dupErr *DuplicateSKUError
	require.True(t, errors.As(err, &dupErr), "got %v", err)

	// Catalog listing: the parent carries its three active variants; children are hidden.
	catalog := WithIncludeVariants(ctx)
	list, total, err := svc.ListItems(catalog, tenantID, "", "", 50, 0, nil, nil, "", nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, 2, total) // T-shirt + TAKEN
	var p *ItemDTO
	for i := range list {
		require.Empty(t, list[i].VariantOf, "child %s leaked into the catalog", list[i].SKU)
		if list[i].ID == parent.ID {
			p = &list[i]
		}
	}
	require.NotNil(t, p)
	require.True(t, p.HasVariants)
	require.Len(t, p.Variants, 3)
	require.Equal(t, "TSHIRT-RED-S", p.Variants[0].SKU)
	require.NotNil(t, p.Variants[0].OnHand) // each child got its own default-warehouse balance
	require.Equal(t, 1200.0, p.Variants[0].Price)

	// include_children=1 lists them as rows too; a plain listing (stock pages) always does.
	_, total, err = svc.ListItems(WithIncludeVariantChildren(catalog), tenantID, "", "", 50, 0, nil, nil, "", nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, 5, total)
	_, total, err = svc.ListItems(ctx, tenantID, "", "", 50, 0, nil, nil, "", nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, 5, total)

	// A barcode search reaches the child even in catalog mode, and the DTO says whose it is.
	found, _, err := svc.ListItems(catalog, tenantID, "", "", 50, 0, nil, nil, "6001000000011", nil, nil, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, parent.ID.String(), found[0].VariantOf)
	require.Equal(t, map[string]string{"colour": "Blue", "size": "S"}, found[0].VariantAttributes)

	// GET /items/{sku} on a child resolves it and names the parent.
	avail, err := svc.GetStockAvailability(ctx, tenantID, "TSHIRT-RED-M")
	require.NoError(t, err)
	require.Equal(t, parent.ID.String(), avail.VariantOf)
	require.Equal(t, "M", avail.VariantAttributes["size"])

	// The variants endpoint returns the children.
	vs, err := svc.ListItemVariants(ctx, tenantID, parent.ID)
	require.NoError(t, err)
	require.Len(t, vs, 3)

	// RefreshVariantAxes (bulk import) rebuilds axes from the active children only.
	require.NoError(t, svc.RefreshVariantAxes(ctx, tenantID, []uuid.UUID{parent.ID}))
	stored, err = client.Item.Get(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"colour": {"Red", "Blue"}, "size": {"S", "M"}}, VariantAxesFromMetadata(stored.Metadata))
	require.Equal(t, item.TypeGOODS, stored.Type)
}
