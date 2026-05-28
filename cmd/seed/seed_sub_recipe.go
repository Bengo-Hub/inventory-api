package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
	entring "github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

// seedSubRecipeDemo creates an "Espresso Base" prep recipe and a "Cappuccino Demo" recipe
// that uses it as a sub-recipe ingredient — demonstrating the sub_recipe_id feature.
func seedSubRecipeDemo(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	// ── 1. Ensure catalog items exist for the demo recipes ─────────────────────
	espBaseItemID := itemUUID(tenantID, "RCP-ESP-BASE")
	capDemoItemID := itemUUID(tenantID, "RCP-CAP-DEMO")

	for _, it := range []struct {
		id   uuid.UUID
		sku  string
		name string
	}{
		{espBaseItemID, "RCP-ESP-BASE", "Espresso Base (Prep)"},
		{capDemoItemID, "RCP-CAP-DEMO", "Cappuccino Demo"},
	} {
		exists, _ := client.Item.Query().Where(entitem.ID(it.id)).Exist(ctx)
		if !exists {
			if _, err := client.Item.Create().
				SetID(it.id).
				SetTenantID(tenantID).
				SetSku(it.sku).
				SetName(it.name).
				SetType(entitem.TypeRECIPE).
				SetIsActive(true).
				SetRequiresAgeVerification(false).
				SetIsPerishable(false).
				SetTrackLots(false).
				SetTrackSerialNumbers(false).
				Save(ctx); err != nil {
				return fmt.Errorf("create item %s: %w", it.sku, err)
			}
			log.Printf("sub-recipe demo item created: %s", it.sku)
		}
	}

	// ── 2. Espresso Base recipe ─────────────────────────────────────────────────
	espRecipeID := recipeUUID(tenantID, "RCP-ESPRESSO-BASE")
	margin := 72.0

	_, espErr := client.Recipe.Get(ctx, espRecipeID)
	if ent.IsNotFound(espErr) {
		if _, err := client.Recipe.Create().
			SetID(espRecipeID).
			SetTenantID(tenantID).
			SetSku("RCP-ESPRESSO-BASE").
			SetName("Espresso Base").
			SetOutputQty(1).
			SetUnitOfMeasure("PORTION").
			SetIsActive(true).
			SetNillableTargetMarginPercent(&margin).
			SetNillableItemID(&espBaseItemID).
			Save(ctx); err != nil {
			return fmt.Errorf("create espresso base recipe: %w", err)
		}
	}

	// Espresso Base ingredient: 18g coffee beans, 5% waste.
	espIngItemID := itemUUID(tenantID, "RAW-ESP-001")
	espIngID := recipeIngredientUUID(espRecipeID, "RAW-ESP-001")
	unitKG := unitUUID("KG")
	if _, err := client.RecipeIngredient.Query().
		Where(entring.RecipeID(espRecipeID), entring.ItemID(espIngItemID)).
		First(ctx); ent.IsNotFound(err) {
		if _, createErr := client.RecipeIngredient.Create().
			SetID(espIngID).
			SetRecipeID(espRecipeID).
			SetItemID(espIngItemID).
			SetItemSku("RAW-ESP-001").
			SetQuantity(0.018).
			SetUnitOfMeasure("KG").
			SetNillableUnitID(&unitKG).
			SetWastePercent(5.0).
			SetDisplayOrder(0).
			Save(ctx); createErr != nil {
			return fmt.Errorf("create espresso base ingredient: %w", createErr)
		}
	}

	// Calculate Espresso Base costs first (Cappuccino Demo depends on them).
	if err := recalcSubRecipeCost(ctx, client, tenantID, espRecipeID, "RCP-ESPRESSO-BASE"); err != nil {
		log.Printf("[WARN] espresso base cost recalc: %v", err)
	}

	// ── 3. Cappuccino Demo recipe ───────────────────────────────────────────────
	capRecipeID := recipeUUID(tenantID, "RCP-CAPPUCCINO")
	capMargin := 72.0

	_, capErr := client.Recipe.Get(ctx, capRecipeID)
	if ent.IsNotFound(capErr) {
		if _, err := client.Recipe.Create().
			SetID(capRecipeID).
			SetTenantID(tenantID).
			SetSku("RCP-CAPPUCCINO").
			SetName("Cappuccino (Sub-recipe Demo)").
			SetOutputQty(1).
			SetUnitOfMeasure("CUP").
			SetIsActive(true).
			SetNillableTargetMarginPercent(&capMargin).
			SetNillableItemID(&capDemoItemID).
			Save(ctx); err != nil {
			return fmt.Errorf("create cappuccino demo recipe: %w", err)
		}
	}

	// Sub-recipe ingredient: Espresso Base ×1 (item_id points to RCP-ESP-BASE item).
	subIngID := recipeIngredientUUID(capRecipeID, "RCP-ESP-BASE")
	if _, err := client.RecipeIngredient.Query().
		Where(entring.RecipeID(capRecipeID), entring.ItemID(espBaseItemID)).
		First(ctx); ent.IsNotFound(err) {
		if _, createErr := client.RecipeIngredient.Create().
			SetID(subIngID).
			SetRecipeID(capRecipeID).
			SetItemID(espBaseItemID).
			SetItemSku("RCP-ESP-BASE").
			SetNillableSubRecipeID(&espRecipeID).
			SetQuantity(1).
			SetUnitOfMeasure("PORTION").
			SetWastePercent(0).
			SetDisplayOrder(0).
			Save(ctx); createErr != nil {
			return fmt.Errorf("create cappuccino sub-recipe ingredient: %w", createErr)
		}
	}

	// Direct milk ingredient: 150ml whole milk, 3% waste.
	milkItemID := itemUUID(tenantID, "RAW-MLK-001")
	milkIngID := recipeIngredientUUID(capRecipeID, "RAW-MLK-001")
	unitLitre := unitUUID("LITRE")
	if _, err := client.RecipeIngredient.Query().
		Where(entring.RecipeID(capRecipeID), entring.ItemID(milkItemID)).
		First(ctx); ent.IsNotFound(err) {
		if _, createErr := client.RecipeIngredient.Create().
			SetID(milkIngID).
			SetRecipeID(capRecipeID).
			SetItemID(milkItemID).
			SetItemSku("RAW-MLK-001").
			SetQuantity(0.150).
			SetUnitOfMeasure("LITRE").
			SetNillableUnitID(&unitLitre).
			SetWastePercent(3.0).
			SetDisplayOrder(1).
			Save(ctx); createErr != nil {
			return fmt.Errorf("create cappuccino milk ingredient: %w", createErr)
		}
	}

	if err := recalcSubRecipeCost(ctx, client, tenantID, capRecipeID, "RCP-CAPPUCCINO"); err != nil {
		log.Printf("[WARN] cappuccino demo cost recalc: %v", err)
	}

	log.Printf("sub-recipe demo seeded: Espresso Base → Cappuccino (Demo)")
	return nil
}

// recalcSubRecipeCost handles cost calculation for sub-recipe-aware recipes in the seed.
func recalcSubRecipeCost(ctx context.Context, client *ent.Client, tenantID, recipeID uuid.UUID, sku string) error {
	r, err := client.Recipe.Query().
		Where(entrecipe.ID(recipeID), entrecipe.TenantID(tenantID)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.WithItem().WithSubRecipe()
		}).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("load recipe: %w", err)
	}

	var totalCost float64
	hasCost := false

	for _, ing := range r.Edges.Ingredients {
		effectiveQty := ing.Quantity * (1 + ing.WastePercent/100)

		if ing.SubRecipeID != nil && ing.Edges.SubRecipe != nil {
			if ing.Edges.SubRecipe.CostPerPortion != nil {
				totalCost += effectiveQty * *ing.Edges.SubRecipe.CostPerPortion
				hasCost = true
			}
			continue
		}

		if ing.Edges.Item == nil || ing.Edges.Item.CostPrice == nil {
			continue
		}
		totalCost += effectiveQty * *ing.Edges.Item.CostPrice
		hasCost = true
	}

	if !hasCost {
		return nil
	}

	outputQty := r.OutputQty
	if outputQty <= 0 {
		outputQty = 1
	}
	costPerPortion := totalCost / outputQty

	marginPct := 30.0
	if r.TargetMarginPercent != nil {
		marginPct = *r.TargetMarginPercent
	} else {
		cfg, cfgErr := client.TenantInventoryConfig.Query().
			Where(entconfig.TenantID(tenantID)).
			Only(ctx)
		if cfgErr == nil && cfg.DefaultTargetMarginPercent != nil {
			marginPct = *cfg.DefaultTargetMarginPercent
		}
	}

	suggestedPrice := costPerPortion / (1 - marginPct/100)

	if _, err := client.Recipe.UpdateOneID(recipeID).
		SetTotalCost(totalCost).
		SetCostPerPortion(costPerPortion).
		SetSuggestedPrice(suggestedPrice).
		Save(ctx); err != nil {
		return fmt.Errorf("update costs for %s: %w", sku, err)
	}

	log.Printf("sub-recipe cost recalculated: %s — total=%.4f cost/portion=%.4f suggested=%.2f",
		sku, totalCost, costPerPortion, suggestedPrice)
	return nil
}
