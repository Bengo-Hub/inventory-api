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

	recipePrice := s.recipeSellingPrices(ctx, tenantID, cfg, itemIDs)
	tierPrice := s.defaultTierPrices(ctx, tenantID, itemIDs)

	inclusiveDefault := cfg != nil && cfg.PricesInclusiveOfTax
	defaultTaxCode := ""
	if cfg != nil {
		defaultTaxCode = cfg.DefaultTaxCode
	}
	rate, rateCode := s.resolveVATRate(ctx, tenantID, defaultTaxCode)
	// A business that isn't VAT-registered must not charge VAT — suppress the rate entirely
	// (including the inclusive-price DefaultVATRate fallback) so no tax is split out.
	suppressVAT := s.vatSuppressed(ctx, tenantID)

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
		if suppressVAT {
			effRate = 0
		} else if effRate <= 0 && inclusive {
			effRate = DefaultVATRate // must still back-compute when treasury is unreachable
		}
		if effRate <= 0 {
			continue // no VAT (exclusive w/ no rate, or suppressed) → leave tax fields unset
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

// EnsureDefaultPrice writes `price` onto the tenant's default pricing tier for an item, creating the
// default RETAIL tier if none exists, and upserting the all-outlets ItemPricing row. Used by bulk
// import so an uploaded selling_price becomes a real tier price the POS pricing-resolve can read.
func (s *Service) EnsureDefaultPrice(ctx context.Context, tenantID, itemID uuid.UUID, price float64) error {
	if price <= 0 {
		return nil
	}
	tier, err := s.client.PricingTier.Query().
		Where(pricingtier.TenantID(tenantID), pricingtier.IsDefault(true), pricingtier.IsActive(true)).
		First(ctx)
	if ent.IsNotFound(err) {
		tier, err = s.client.PricingTier.Create().
			SetTenantID(tenantID).SetName("Retail").SetCode("RETAIL").
			SetIsDefault(true).SetIsActive(true).SetSortOrder(0).
			Save(ctx)
	}
	if err != nil {
		return err
	}
	if existing, qErr := s.client.ItemPricing.Query().
		Where(
			itempricing.TenantID(tenantID),
			itempricing.ItemID(itemID),
			itempricing.PricingTierID(tier.ID),
			itempricing.OutletIDIsNil(),
		).
		First(ctx); qErr == nil && existing != nil {
		_, err = existing.Update().SetPrice(price).SetIsActive(true).Save(ctx)
		return err
	}
	_, err = s.client.ItemPricing.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetPricingTierID(tier.ID).
		SetPrice(price).
		SetCurrency("KES").
		SetIsActive(true).
		Save(ctx)
	return err
}

// recipeSellingPrices maps RECIPE item_id → selling price. Resolution order:
// explicit recipe selling_price → recipe suggested_price → cost_per_portion at the recipe/tenant
// target margin (so a fully-costed menu item is NEVER silently priced 0 and hidden on the POS — the
// merchant can still override with an explicit price/tier). cfg may be nil.
func (s *Service) recipeSellingPrices(ctx context.Context, tenantID uuid.UUID, cfg *ent.TenantInventoryConfig, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
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
		switch {
		case r.SellingPrice != nil && *r.SellingPrice > 0:
			out[*r.ItemID] = *r.SellingPrice
		case r.SuggestedPrice != nil && *r.SuggestedPrice > 0:
			if _, ok := out[*r.ItemID]; !ok {
				out[*r.ItemID] = *r.SuggestedPrice
			}
		case r.CostPerPortion != nil && *r.CostPerPortion > 0:
			// Derive a sensible menu price from plate cost at the target margin (recipe override →
			// tenant default → 70% margin ≈ 30% food cost, the industry norm). Prevents costed
			// recipes from defaulting to KES 0 / unavailable.
			margin := 70.0
			if r.TargetMarginPercent != nil && *r.TargetMarginPercent > 0 && *r.TargetMarginPercent < 100 {
				margin = *r.TargetMarginPercent
			} else if cfg != nil && cfg.DefaultTargetMarginPercent != nil && *cfg.DefaultTargetMarginPercent > 0 && *cfg.DefaultTargetMarginPercent < 100 {
				margin = *cfg.DefaultTargetMarginPercent
			}
			if _, ok := out[*r.ItemID]; !ok {
				out[*r.ItemID] = *r.CostPerPortion / (1 - margin/100)
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
// vatSuppressed reports whether VAT must NOT be charged because the business isn't
// VAT-registered. Permissive: false (charge VAT) unless the resolver affirmatively says
// the tenant is not VAT-active. Uses an optional interface so resolver mocks need not change.
func (s *Service) vatSuppressed(ctx context.Context, tenantID uuid.UUID) bool {
	if s.taxResolver == nil {
		return false
	}
	if va, ok := s.taxResolver.(interface {
		VATActive(context.Context, uuid.UUID) bool
	}); ok {
		return !va.VATActive(ctx, tenantID)
	}
	return false
}

func (s *Service) resolveVATRate(ctx context.Context, tenantID uuid.UUID, preferredCode string) (float64, string) {
	if s.taxResolver == nil {
		return 0, preferredCode
	}
	if r, c, ok := s.taxResolver.ResolveVATRate(ctx, tenantID, preferredCode); ok {
		return r, c
	}
	return 0, preferredCode
}
