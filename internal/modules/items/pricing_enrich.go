package items

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itempricing"
	"github.com/bengobox/inventory-service/internal/ent/pricingtier"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
)

// enrichPrices populates SellingPrice + the tax split (NetPrice/TaxAmount/TaxRate) on the given
// DTOs so the POS/ordering proxies receive a usable customer price and tax. The effective price
// is resolved as: recipe selling price (RECIPE items) → default pricing-tier price → existing
// cost+margin suggestion. When the tenant (or the item) treats prices as tax-inclusive, the tax
// is computed backwards using the VAT rate resolved from treasury-api (falling back to
// DefaultVATRate when treasury is unavailable). cfg may be nil.
func (s *Service) enrichPrices(ctx context.Context, tenantID uuid.UUID, cfg *ent.TenantInventoryConfig, dtos []ItemDTO) {
	if len(dtos) == 0 {
		return
	}
	itemIDs := make([]uuid.UUID, len(dtos))
	for i := range dtos {
		itemIDs[i] = dtos[i].ID
	}

	recipePrice := s.recipeSellingPrices(ctx, tenantID, itemIDs)
	tierPrice := s.defaultTierPrices(ctx, tenantID, itemIDs)

	inclusiveDefault := cfg != nil && cfg.PricesInclusiveOfTax
	defaultTaxCode := ""
	if cfg != nil {
		defaultTaxCode = cfg.DefaultTaxCode
	}
	rate, rateCode := s.resolveVATRate(ctx, tenantID, defaultTaxCode)

	for i := range dtos {
		d := &dtos[i]
		price := effectivePrice(d, recipePrice, tierPrice)
		if price <= 0 {
			continue
		}
		sp := price
		d.SellingPrice = &sp

		inclusive := inclusiveDefault || d.TaxInclusive
		effRate := rate
		if effRate <= 0 && inclusive {
			effRate = DefaultVATRate // must still back-compute when treasury is unreachable
		}
		if effRate <= 0 {
			continue // exclusive item with no known rate → leave tax fields unset
		}
		split := ComputeTaxSplit(price, effRate, inclusive)
		net, tax, r := split.Net, split.Tax, split.Rate
		d.NetPrice = &net
		d.TaxAmount = &tax
		d.TaxRate = &r
		d.TaxInclusive = inclusive
		if d.TaxCodeID == "" {
			if rateCode != "" {
				d.TaxCodeID = rateCode
			} else if defaultTaxCode != "" {
				d.TaxCodeID = defaultTaxCode
			}
		}
	}
}

// effectivePrice resolves an item's customer price: recipe → default tier → cost+margin suggestion.
func effectivePrice(d *ItemDTO, recipePrice, tierPrice map[uuid.UUID]float64) float64 {
	if p, ok := recipePrice[d.ID]; ok && p > 0 {
		return p
	}
	if p, ok := tierPrice[d.ID]; ok && p > 0 {
		return p
	}
	if d.SuggestedPrice != nil && *d.SuggestedPrice > 0 {
		return *d.SuggestedPrice
	}
	return 0
}

// recipeSellingPrices maps RECIPE item_id → selling price (or suggested price fallback).
func (s *Service) recipeSellingPrices(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true), recipe.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return out
	}
	for _, r := range recs {
		if r.ItemID == nil {
			continue
		}
		if r.SellingPrice != nil && *r.SellingPrice > 0 {
			out[*r.ItemID] = *r.SellingPrice
		} else if r.SuggestedPrice != nil && *r.SuggestedPrice > 0 {
			if _, ok := out[*r.ItemID]; !ok {
				out[*r.ItemID] = *r.SuggestedPrice
			}
		}
	}
	return out
}

// defaultTierPrices maps item_id → price from the tenant's default pricing tier, falling back
// to any active tier's price when no default tier is configured.
func (s *Service) defaultTierPrices(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	var defaultTierID uuid.UUID
	if t, err := s.client.PricingTier.Query().
		Where(pricingtier.TenantID(tenantID), pricingtier.IsDefault(true), pricingtier.IsActive(true)).
		First(ctx); err == nil {
		defaultTierID = t.ID
	}
	prices, err := s.client.ItemPricing.Query().
		Where(itempricing.TenantID(tenantID), itempricing.IsActive(true), itempricing.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return out
	}
	firstSeen := map[uuid.UUID]float64{}
	for _, p := range prices {
		if p.Price <= 0 {
			continue
		}
		if _, ok := firstSeen[p.ItemID]; !ok {
			firstSeen[p.ItemID] = p.Price
		}
		if defaultTierID != uuid.Nil && p.PricingTierID == defaultTierID {
			out[p.ItemID] = p.Price // default tier wins
		}
	}
	for id, p := range firstSeen {
		if _, ok := out[id]; !ok {
			out[id] = p
		}
	}
	return out
}

// resolveVATRate resolves the VAT rate (%) + code via the treasury resolver; (0, preferredCode)
// when no resolver is configured or treasury is unavailable.
func (s *Service) resolveVATRate(ctx context.Context, tenantID uuid.UUID, preferredCode string) (float64, string) {
	if s.taxResolver == nil {
		return 0, preferredCode
	}
	if r, c, ok := s.taxResolver.ResolveVATRate(ctx, tenantID, preferredCode); ok {
		return r, c
	}
	return 0, preferredCode
}
