package items

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/testutil"
)

// capGuard stands in for the handler's plan check: it records every request and refuses while
// full is set, the way the 402 refusal does once inventory_max_sku is reached.
type capGuard struct {
	full  bool
	calls []int
}

func (g *capGuard) ctx() context.Context {
	return WithActivationGuard(context.Background(), func(_ context.Context, n int) error {
		g.calls = append(g.calls, n)
		if g.full {
			return ErrActivationRefused
		}
		return nil
	})
}

// TestReactivationChecksThePlanProductLimit: every path that turns an inactive or End of Life item
// active asks the guard first and writes nothing when it refuses; an item that was already active,
// or stays inactive, never asks, so editing it at the cap still works.
func TestReactivationChecksThePlanProductLimit(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_reactivate_")
	bg := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID := uuid.New()
	_, err := client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-" + tenantID.String()[:8]).Save(bg)
	require.NoError(t, err)
	client.Warehouse.Create().SetTenantID(tenantID).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(bg)

	mk := func(sku string, active bool) *ItemDTO {
		dto, err := svc.CreateItem(bg, tenantID, ItemDTO{SKU: sku, Name: sku, Type: "GOODS", IsActive: active})
		require.NoError(t, err)
		return dto
	}
	isActive := func(id uuid.UUID) bool { return client.Item.GetX(bg, id).IsActive }
	edit := func(ctx context.Context, id uuid.UUID, active bool, name string) error {
		dto := *svc.mapToDTO(client.Item.GetX(bg, id))
		dto.IsActive, dto.Name = active, name
		_, err := svc.UpdateItem(ctx, tenantID, id, dto)
		return err
	}

	live := mk("LIVE", true)
	archived := mk("ARCHIVED", false)
	g := &capGuard{full: true}

	// At the cap: editing an already-active item, or an inactive item that stays inactive, works
	// and never asks.
	require.NoError(t, edit(g.ctx(), live.ID, true, "Live renamed"))
	require.NoError(t, edit(g.ctx(), archived.ID, false, "Archived renamed"))
	require.Empty(t, g.calls)

	// At the cap: reactivating through UpdateItem is refused and the item stays inactive.
	err = edit(g.ctx(), archived.ID, true, "Archived back")
	require.True(t, errors.Is(err, ErrActivationRefused), "got %v", err)
	require.False(t, isActive(archived.ID))
	require.Equal(t, "Archived renamed", client.Item.GetX(bg, archived.ID).Name)
	require.Equal(t, []int{1}, g.calls)

	// Below the cap it goes through.
	g.full, g.calls = false, nil
	require.NoError(t, edit(g.ctx(), archived.ID, true, "Archived back"))
	require.True(t, isActive(archived.ID))
	require.Equal(t, []int{1}, g.calls)

	// End of Life restore.
	eol := mk("EOL", true)
	_, err = svc.MarkItemEOL(bg, tenantID, "EOL")
	require.NoError(t, err)
	g.full, g.calls = true, nil
	_, err = svc.RestoreItemEOL(g.ctx(), tenantID, "EOL")
	require.True(t, errors.Is(err, ErrActivationRefused), "got %v", err)
	stored := client.Item.GetX(bg, eol.ID)
	require.False(t, stored.IsActive)
	require.NotNil(t, stored.EndOfLifeAt)
	g.full = false
	_, err = svc.RestoreItemEOL(g.ctx(), tenantID, "EOL")
	require.NoError(t, err)
	require.True(t, isActive(eol.ID))
	require.Equal(t, []int{1, 1}, g.calls)

	// Bulk activate asks once for the items it would actually turn on: the active one and the End
	// of Life one are skipped, so two of four.
	off1, off2 := mk("OFF1", false), mk("OFF2", false)
	eol2 := mk("EOL2", true)
	_, err = svc.MarkItemEOL(bg, tenantID, "EOL2")
	require.NoError(t, err)
	ids := []uuid.UUID{off1.ID, off2.ID, live.ID, eol2.ID}
	g.full, g.calls = true, nil
	_, err = svc.BulkItemAction(g.ctx(), tenantID, ids, BulkActionActivate)
	require.True(t, errors.Is(err, ErrActivationRefused), "got %v", err)
	require.False(t, isActive(off1.ID))
	require.False(t, isActive(off2.ID))
	require.Equal(t, []int{2}, g.calls)
	g.full = false
	res, err := svc.BulkItemAction(g.ctx(), tenantID, ids, BulkActionActivate)
	require.NoError(t, err)
	require.Equal(t, 2, res.Processed)
	require.True(t, isActive(off1.ID))
	require.True(t, isActive(off2.ID))

	// Deactivating never asks, even at the cap.
	g.full, g.calls = true, nil
	_, err = svc.BulkItemAction(g.ctx(), tenantID, []uuid.UUID{off1.ID}, BulkActionDeactivate)
	require.NoError(t, err)
	require.Empty(t, g.calls)

	// Variant matrix: a resave that turns a dropped child back on asks once, as a batch.
	parent := mk("SHIRT", true)
	axes := map[string][]string{"size": {"S", "M"}}
	rowS := VariantRowInput{Attributes: map[string]string{"size": "S"}}
	rowM := VariantRowInput{Attributes: map[string]string{"size": "M"}}
	_, err = svc.SaveVariants(bg, tenantID, parent.ID, SaveVariantsInput{Axes: axes, Rows: []VariantRowInput{rowS, rowM}})
	require.NoError(t, err)
	_, err = svc.SaveVariants(bg, tenantID, parent.ID, SaveVariantsInput{Axes: axes, Rows: []VariantRowInput{rowS}})
	require.NoError(t, err)
	all, err := client.Item.Query().All(bg)
	require.NoError(t, err)
	var mID uuid.UUID
	for _, c := range all {
		if pid, ok := VariantOfFromMetadata(c.Metadata); ok && pid == parent.ID &&
			VariantAttributesFromMetadata(c.Metadata)["size"] == "M" {
			mID = c.ID
		}
	}
	require.NotEqual(t, uuid.Nil, mID)
	require.False(t, isActive(mID))

	g.full, g.calls = true, nil
	_, err = svc.SaveVariants(g.ctx(), tenantID, parent.ID, SaveVariantsInput{Axes: axes, Rows: []VariantRowInput{rowS, rowM}})
	require.True(t, errors.Is(err, ErrActivationRefused), "got %v", err)
	require.False(t, isActive(mID))
	require.Equal(t, []int{1}, g.calls)
	g.full, g.calls = false, nil
	_, err = svc.SaveVariants(g.ctx(), tenantID, parent.ID, SaveVariantsInput{Axes: axes, Rows: []VariantRowInput{rowS, rowM}})
	require.NoError(t, err)
	require.True(t, isActive(mID))
	require.Equal(t, []int{1}, g.calls, "the child's own UpdateItem must not ask a second time")

	// A save that adds a new child and turns a dropped one back on asks once for both. At the cap
	// the whole save is refused and nothing is written: no new child, M stays off, axes unchanged.
	_, err = svc.SaveVariants(bg, tenantID, parent.ID, SaveVariantsInput{Axes: axes, Rows: []VariantRowInput{rowS}})
	require.NoError(t, err)
	require.False(t, isActive(mID))
	children := func() int {
		n := 0
		for _, c := range client.Item.Query().AllX(bg) {
			if pid, ok := VariantOfFromMetadata(c.Metadata); ok && pid == parent.ID {
				n++
			}
		}
		return n
	}
	before := children()
	bigger := map[string][]string{"size": {"S", "M", "L"}}
	rowL := VariantRowInput{Attributes: map[string]string{"size": "L"}}
	off := false
	rowXL := VariantRowInput{Attributes: map[string]string{"size": "L"}, IsActive: &off}

	g.full, g.calls = true, nil
	_, err = svc.SaveVariants(g.ctx(), tenantID, parent.ID, SaveVariantsInput{Axes: bigger, Rows: []VariantRowInput{rowS, rowM, rowL}})
	require.True(t, errors.Is(err, ErrActivationRefused), "got %v", err)
	require.Equal(t, []int{2}, g.calls)
	require.Equal(t, before, children())
	require.False(t, isActive(mID))
	require.Equal(t, axes, VariantAxesFromMetadata(client.Item.GetX(bg, parent.ID).Metadata))

	// A new child saved inactive takes no slot, so at the cap it is still allowed.
	g.calls = nil
	_, err = svc.SaveVariants(g.ctx(), tenantID, parent.ID, SaveVariantsInput{Axes: bigger, Rows: []VariantRowInput{rowS, rowXL}})
	require.NoError(t, err)
	require.Empty(t, g.calls)
	require.Equal(t, before+1, children())

	// Below the cap the batch goes through.
	rowM2 := VariantRowInput{Attributes: map[string]string{"size": "M"}}
	rowL2 := VariantRowInput{Attributes: map[string]string{"size": "L"}}
	g.full, g.calls = false, nil
	_, err = svc.SaveVariants(g.ctx(), tenantID, parent.ID, SaveVariantsInput{Axes: bigger, Rows: []VariantRowInput{rowS, rowM2, rowL2}})
	require.NoError(t, err)
	require.Equal(t, []int{2}, g.calls, "M and L both come back on")
	require.True(t, isActive(mID))
}
