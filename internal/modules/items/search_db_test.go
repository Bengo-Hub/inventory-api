package items

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// TestListItemsSearchMatchesSKUBarcodeGTINAndName pins the "Fix" requirement from the
// debounced-search plan (Find, Flip, Fix): on the server path an item is found by any of its
// codes, not only its title, a LIKE wildcard in the term matches literally, and another
// tenant's items never appear.
func TestListItemsSearchMatchesSKUBarcodeGTINAndName(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_itemsearch_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tenantID, other} {
		_, err := client.Tenant.Create().SetID(id).SetName("Shop").SetSlug("shop-" + id.String()[:8]).Save(ctx)
		require.NoError(t, err)
	}
	mk := func(tid uuid.UUID, sku, name, barcode, gtin string) uuid.UUID {
		c := client.Item.Create().SetTenantID(tid).SetSku(sku).SetName(name).SetType(item.TypeGOODS)
		if barcode != "" {
			c.SetBarcode(barcode)
		}
		if gtin != "" {
			c.SetGtin(gtin)
		}
		it, err := c.Save(ctx)
		require.NoError(t, err)
		return it.ID
	}
	soap := mk(tenantID, "SKU-ZX481", "Bar soap", "", "")
	milk := mk(tenantID, "MLK-001", "Fresh milk 500ml", "6009801234567", "")
	rice := mk(tenantID, "RCE-002", "Basmati rice", "", "00012345678905")
	percent := mk(tenantID, "PCT-1", "Discount 50% bundle", "", "")
	mk(other, "SKU-ZX481-OTHER", "Bar soap", "6009801234567", "")

	find := func(term string) []uuid.UUID {
		t.Helper()
		rows, _, err := svc.ListItems(ctx, tenantID, "", "", 20, 0, nil, nil, term, nil, nil, "")
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}

	require.Equal(t, []uuid.UUID{soap}, find("zx481"), "SKU fragment, any case")
	require.Equal(t, []uuid.UUID{milk}, find("6009801234567"), "barcode only")
	require.Equal(t, []uuid.UUID{rice}, find("12345678905"), "GTIN only")
	require.Equal(t, []uuid.UUID{milk}, find("fresh MILK"), "name, any case")
	require.Equal(t, []uuid.UUID{percent}, find("50%"), "a % in the term matches literally")
	require.Empty(t, find("qq-no-such-item"), "no match returns nothing")
}
