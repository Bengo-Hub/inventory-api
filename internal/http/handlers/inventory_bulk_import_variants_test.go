package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/bengobox/inventory-service/internal/modules/items"
)

// TestImportVariantColumns: variant_attributes parses into metadata, variant_of_sku resolves
// through the SKU map, half-filled or unknown parents fail the row, and a re-import keeps the
// stored metadata.
func TestImportVariantColumns(t *testing.T) {
	require.Equal(t, map[string]any{"size": "M", "colour": "Red"}, parseVariantAttributes(" Size = M ;colour=Red;;bad;x="))

	parentID := uuid.New()
	skuToID := map[string]uuid.UUID{"TSHIRT": parentID}
	cols := func(vals map[string]string) func([]string, string) string {
		return func(_ []string, name string) string { return vals[name] }
	}

	row := cols(map[string]string{"sku": "TSHIRT-RED-M", "name": "T-shirt, Red / M", "variant_of_sku": "TSHIRT", "variant_attributes": "size=M;colour=Red"})
	dto := buildItemDTOFromRow(row, nil, nil, nil, nil)
	got, err := linkImportVariant(row, nil, &dto, skuToID)
	require.NoError(t, err)
	require.Equal(t, parentID, *got)
	require.Equal(t, parentID.String(), dto.Metadata[items.MetaVariantOf])

	plain := cols(map[string]string{"sku": "MUG", "name": "Mug"})
	dto = buildItemDTOFromRow(plain, nil, nil, nil, nil)
	got, err = linkImportVariant(plain, nil, &dto, skuToID)
	require.NoError(t, err)
	require.Nil(t, got)

	half := cols(map[string]string{"sku": "X", "name": "X", "variant_of_sku": "TSHIRT"})
	dto = buildItemDTOFromRow(half, nil, nil, nil, nil)
	_, err = linkImportVariant(half, nil, &dto, skuToID)
	require.Error(t, err)

	orphan := cols(map[string]string{"sku": "X", "name": "X", "variant_of_sku": "NOPE", "variant_attributes": "size=S"})
	dto = buildItemDTOFromRow(orphan, nil, nil, nil, nil)
	_, err = linkImportVariant(orphan, nil, &dto, skuToID)
	require.Error(t, err)

	dto = items.ItemDTO{Metadata: map[string]any{items.MetaVariantAttributes: map[string]any{"size": "L"}}}
	keepMetadataOnImport(&dto, map[string]any{"note": "kept", items.MetaVariantAttributes: map[string]any{"size": "M"}})
	require.Equal(t, "kept", dto.Metadata["note"])
	require.Equal(t, map[string]any{"size": "L"}, dto.Metadata[items.MetaVariantAttributes])
}
