package main

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entbundle "github.com/bengobox/inventory-service/internal/ent/bundle"
	entbundlecomponent "github.com/bengobox/inventory-service/internal/ent/bundlecomponent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entitempricing "github.com/bengobox/inventory-service/internal/ent/itempricing"
	entpricingtier "github.com/bengobox/inventory-service/internal/ent/pricingtier"
)

// ddrPerDelegatePerDay is the seeded Day Delegate Rate (KES per delegate per day).
const ddrPerDelegatePerDay = 3500.0

// seedConferenceBundle builds a demo conference DDR package for the tenant:
//   - resolves/creates a default pricing tier
//   - prices the CONF-DDR-001 item per_delegate_per_day (so pos-api derives the event total)
//   - creates the Bundle (package_type=DDR, price_basis=per_delegate_per_day) with
//     MEAL_PERIOD components (breakfast, lunch, pm_break) that drive delegate meal-card generation
//
// Idempotent: safe to re-run. The CONF-DDR-001 item itself is seeded as a normal itemDef
// (so cleanupStaleItems spares it); this only adds the pricing + bundle + components.
func seedConferenceBundle(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	// 1) Default pricing tier.
	tier, err := client.PricingTier.Query().
		Where(entpricingtier.TenantID(tenantID), entpricingtier.IsDefault(true), entpricingtier.IsActive(true)).
		First(ctx)
	if ent.IsNotFound(err) {
		tier, err = client.PricingTier.Create().
			SetTenantID(tenantID).
			SetName("Retail").
			SetCode("RETAIL").
			SetIsDefault(true).
			SetIsActive(true).
			Save(ctx)
	}
	if err != nil {
		return err
	}

	// 2) Resolve the bundle item (seeded as an itemDef).
	bundleItem, err := client.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.Sku("CONF-DDR-001")).Only(ctx)
	if err != nil {
		log.Printf("[WARN] seedConferenceBundle: CONF-DDR-001 item not found (skipping): %v", err)
		return nil
	}

	// 3) Price the bundle item per-delegate-per-day on the default tier (idempotent upsert).
	existingPricing, perr := client.ItemPricing.Query().
		Where(
			entitempricing.TenantID(tenantID),
			entitempricing.ItemID(bundleItem.ID),
			entitempricing.PricingTierID(tier.ID),
		).First(ctx)
	switch {
	case ent.IsNotFound(perr):
		if _, cerr := client.ItemPricing.Create().
			SetTenantID(tenantID).
			SetItemID(bundleItem.ID).
			SetPricingTierID(tier.ID).
			SetTierBasis(entitempricing.TierBasisPerDelegatePerDay).
			SetPrice(ddrPerDelegatePerDay).
			SetCurrency("KES").
			SetIsActive(true).
			Save(ctx); cerr != nil {
			return cerr
		}
	case perr != nil:
		return perr
	default:
		if _, uerr := existingPricing.Update().
			SetTierBasis(entitempricing.TierBasisPerDelegatePerDay).
			SetPrice(ddrPerDelegatePerDay).
			SetIsActive(true).
			Save(ctx); uerr != nil {
			return uerr
		}
	}

	// 4) Bundle (idempotent upsert by item_id).
	minDel := 10
	bundle, berr := client.Bundle.Query().
		Where(entbundle.TenantID(tenantID), entbundle.ItemID(bundleItem.ID)).First(ctx)
	switch {
	case ent.IsNotFound(berr):
		bundle, berr = client.Bundle.Create().
			SetTenantID(tenantID).
			SetItemID(bundleItem.ID).
			SetName("Day Delegate Package (DDR)").
			SetPackageType(entbundle.PackageTypeDDR).
			SetPriceBasis(entbundle.PriceBasisPerDelegatePerDay).
			SetMinDelegates(minDel).
			SetIsActive(true).
			Save(ctx)
		if berr != nil {
			return berr
		}
	case berr != nil:
		return berr
	default:
		if _, uerr := bundle.Update().
			SetPackageType(entbundle.PackageTypeDDR).
			SetPriceBasis(entbundle.PriceBasisPerDelegatePerDay).
			SetMinDelegates(minDel).
			SetIsActive(true).
			Save(ctx); uerr != nil {
			return uerr
		}
	}

	// 5) MEAL_PERIOD components — rebuild deterministically.
	if _, derr := client.BundleComponent.Delete().
		Where(entbundlecomponent.BundleID(bundle.ID)).Exec(ctx); derr != nil {
		return derr
	}
	meals := []struct {
		sku        string
		mealPeriod entbundlecomponent.MealPeriod
		sort       int
	}{
		{"BRK-FUL-001", entbundlecomponent.MealPeriodBreakfast, 1},
		{"MIN-GRL-002", entbundlecomponent.MealPeriodLunch, 2},
		{"BEV-TEA-001", entbundlecomponent.MealPeriodPmBreak, 3},
	}
	for _, m := range meals {
		comp, cerr := client.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.Sku(m.sku)).Only(ctx)
		if cerr != nil {
			log.Printf("[WARN] seedConferenceBundle: meal item %s not found, skipping component", m.sku)
			continue
		}
		if _, cerr := client.BundleComponent.Create().
			SetBundleID(bundle.ID).
			SetComponentItemID(comp.ID).
			SetComponentKind(entbundlecomponent.ComponentKindMEAL_PERIOD).
			SetMealPeriod(m.mealPeriod).
			SetQuantity(1).
			SetSortOrder(m.sort).
			Save(ctx); cerr != nil {
			return cerr
		}
	}

	log.Printf("✅ conference DDR bundle seeded (item=%s, %d meal periods, KES %.0f/delegate/day)", bundleItem.Sku, len(meals), ddrPerDelegatePerDay)
	return nil
}