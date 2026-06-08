package main

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitempricing "github.com/bengobox/inventory-service/internal/ent/itempricing"
	entpricingtier "github.com/bengobox/inventory-service/internal/ent/pricingtier"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
)

// defaultSeedTiers are the pricing tiers every seeded tenant receives so the POS pricing-profile
// feature (Retail/Wholesale) works out of the box.
var defaultSeedTiers = []struct {
	Name      string
	Code      string
	IsDefault bool
	SortOrder int
}{
	{"Retail", "RETAIL", true, 0},
	{"Wholesale", "WHOLESALE", false, 1},
}

// seedPricingTiers creates the default Retail/Wholesale pricing tiers for the tenant (idempotent).
func seedPricingTiers(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, t := range defaultSeedTiers {
		exists, err := client.PricingTier.Query().
			Where(entpricingtier.TenantID(tenantID), entpricingtier.Code(t.Code)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := client.PricingTier.Create().
			SetTenantID(tenantID).
			SetName(t.Name).
			SetCode(t.Code).
			SetIsDefault(t.IsDefault).
			SetIsActive(true).
			SetSortOrder(t.SortOrder).
			Save(ctx); err != nil {
			return err
		}
		log.Printf("pricing tier seeded: %s", t.Code)
	}
	return nil
}

// seedItemPricing backfills an ItemPricing row on the default (Retail) tier for every recipe item
// that has a suggested selling price, so seeded menu items carry a real tier price the POS
// pricing-resolve can read (mirrors the bulk-import EnsureDefaultPrice behaviour).
func seedItemPricing(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	tier, err := client.PricingTier.Query().
		Where(entpricingtier.TenantID(tenantID), entpricingtier.IsDefault(true), entpricingtier.IsActive(true)).
		First(ctx)
	if err != nil {
		return err // tiers must be seeded first
	}
	recipes, err := client.Recipe.Query().
		Where(entrecipe.TenantID(tenantID), entrecipe.IsActive(true)).
		All(ctx)
	if err != nil {
		return err
	}
	created := 0
	for _, r := range recipes {
		if r.ItemID == nil || r.SuggestedPrice == nil || *r.SuggestedPrice <= 0 {
			continue
		}
		exists, err := client.ItemPricing.Query().
			Where(
				entitempricing.TenantID(tenantID),
				entitempricing.ItemID(*r.ItemID),
				entitempricing.PricingTierID(tier.ID),
				entitempricing.OutletIDIsNil(),
			).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := client.ItemPricing.Create().
			SetTenantID(tenantID).
			SetItemID(*r.ItemID).
			SetPricingTierID(tier.ID).
			SetPrice(*r.SuggestedPrice).
			SetCurrency("KES").
			SetIsActive(true).
			Save(ctx); err != nil {
			return err
		}
		created++
	}
	if created > 0 {
		log.Printf("item pricing seeded: %d recipe items priced on RETAIL tier", created)
	}
	return nil
}
