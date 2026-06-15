package main

import (
	"context"
	"log"
	"math"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entitempricing "github.com/bengobox/inventory-service/internal/ent/itempricing"
	entpricingtier "github.com/bengobox/inventory-service/internal/ent/pricingtier"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
)

// Default markups used when backfilling demo/seed prices so the POS pricing-profile feature is
// testable out of the box: goods retail = cost × retailMarkup; wholesale = retail × wholesaleFactor.
const (
	retailMarkup    = 1.6
	wholesaleFactor = 0.9
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

// ensureItemPrice creates an ItemPricing row (tenant-wide, no outlet) for an item on a tier when
// one doesn't already exist. Returns whether it created a row.
func ensureItemPrice(ctx context.Context, client *ent.Client, tenantID, itemID, tierID uuid.UUID, price float64) (bool, error) {
	if price <= 0 {
		return false, nil
	}
	exists, err := client.ItemPricing.Query().
		Where(
			entitempricing.TenantID(tenantID),
			entitempricing.ItemID(itemID),
			entitempricing.PricingTierID(tierID),
			entitempricing.OutletIDIsNil(),
		).
		Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if _, err := client.ItemPricing.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetPricingTierID(tierID).
		SetPrice(math.Ceil(price)).
		SetCurrency("KES").
		SetIsActive(true).
		Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// seedItemPricing backfills ItemPricing rows so EVERY sellable seeded item carries a price on BOTH
// the Retail (default) and Wholesale tiers — making the POS pricing-profile switch testable out of
// the box. Idempotent: existing prices are never overwritten.
//
//	1. Recipe items  → RETAIL = recipe suggested_price.
//	2. Goods items   → RETAIL = cost_price × retailMarkup (when no retail price already set).
//	3. Every item that has a RETAIL price but no WHOLESALE price → WHOLESALE = retail × wholesaleFactor.
func seedItemPricing(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	retailTier, err := client.PricingTier.Query().
		Where(entpricingtier.TenantID(tenantID), entpricingtier.IsDefault(true), entpricingtier.IsActive(true)).
		First(ctx)
	if err != nil {
		return err // tiers must be seeded first
	}
	wholesaleTier, _ := client.PricingTier.Query().
		Where(entpricingtier.TenantID(tenantID), entpricingtier.Code("WHOLESALE"), entpricingtier.IsActive(true)).
		First(ctx)

	created := 0

	// 1. Recipe items → RETAIL from suggested_price.
	recipes, err := client.Recipe.Query().
		Where(entrecipe.TenantID(tenantID), entrecipe.IsActive(true)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, r := range recipes {
		if r.ItemID == nil || r.SuggestedPrice == nil || *r.SuggestedPrice <= 0 {
			continue
		}
		ok, err := ensureItemPrice(ctx, client, tenantID, *r.ItemID, retailTier.ID, *r.SuggestedPrice)
		if err != nil {
			return err
		}
		if ok {
			created++
		}
	}

	// 2. Goods items → RETAIL from cost_price markup (only when no retail price exists yet).
	goods, err := client.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IsActive(true), entitem.TypeEQ(entitem.TypeGOODS)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, it := range goods {
		if it.CostPrice == nil || *it.CostPrice <= 0 {
			continue
		}
		ok, err := ensureItemPrice(ctx, client, tenantID, it.ID, retailTier.ID, *it.CostPrice*retailMarkup)
		if err != nil {
			return err
		}
		if ok {
			created++
		}
	}

	// 3. Mirror every RETAIL price onto the WHOLESALE tier at a discount (when missing).
	wholesaleCreated := 0
	if wholesaleTier != nil {
		retailPrices, err := client.ItemPricing.Query().
			Where(
				entitempricing.TenantID(tenantID),
				entitempricing.PricingTierID(retailTier.ID),
				entitempricing.IsActive(true),
				entitempricing.OutletIDIsNil(),
			).
			All(ctx)
		if err != nil {
			return err
		}
		for _, rp := range retailPrices {
			ok, err := ensureItemPrice(ctx, client, tenantID, rp.ItemID, wholesaleTier.ID, rp.Price*wholesaleFactor)
			if err != nil {
				return err
			}
			if ok {
				wholesaleCreated++
			}
		}
	}

	if created > 0 || wholesaleCreated > 0 {
		log.Printf("item pricing seeded: %d retail, %d wholesale", created, wholesaleCreated)
	}
	return nil
}
