package items

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/modifiergroup"
	"github.com/bengobox/inventory-service/internal/ent/modifieroption"
)

// enrichModifierGroups attaches each item's active modifier groups + options (e.g. "Extra
// Honey" on a Dawa) to its DTO in one batched query — mirrors enrichPrices' batch-by-itemIDs
// shape. Best-effort: a query failure leaves ModifierGroups unset rather than failing the
// whole item list, since a catalog read must never break because of a modifier lookup.
func (s *Service) enrichModifierGroups(ctx context.Context, dtos []ItemDTO) {
	if len(dtos) == 0 {
		return
	}
	itemIDs := make([]uuid.UUID, len(dtos))
	for i := range dtos {
		itemIDs[i] = dtos[i].ID
	}

	groups, err := s.client.ModifierGroup.Query().
		Where(modifiergroup.ItemIDIn(itemIDs...)).
		WithOptions(func(oq *ent.ModifierOptionQuery) {
			oq.Where(modifieroption.IsActive(true)).
				Order(ent.Asc(modifieroption.FieldDisplayOrder))
		}).
		Order(ent.Asc(modifiergroup.FieldDisplayOrder)).
		All(ctx)
	if err != nil || len(groups) == 0 {
		return
	}

	byItem := make(map[uuid.UUID][]ItemModifierGroup)
	for _, g := range groups {
		opts := make([]ItemModifierOption, 0, len(g.Edges.Options))
		for _, o := range g.Edges.Options {
			opts = append(opts, ItemModifierOption{
				ID:              o.ID,
				Name:            o.Name,
				SKU:             o.Sku,
				PriceAdjustment: o.PriceAdjustment,
				IsDefault:       o.IsDefault,
			})
		}
		byItem[g.ItemID] = append(byItem[g.ItemID], ItemModifierGroup{
			ID:            g.ID,
			Name:          g.Name,
			IsRequired:    g.IsRequired,
			MinSelections: g.MinSelections,
			MaxSelections: g.MaxSelections,
			Options:       opts,
		})
	}

	for i := range dtos {
		if gs, ok := byItem[dtos[i].ID]; ok {
			dtos[i].ModifierGroups = gs
		}
	}
}
