package recipes

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
)

// RecalculateRecipeCosts computes and persists total_cost, cost_per_portion, and suggested_price
// for a recipe by summing ingredient cost_prices (and sub-recipe cost_per_portion) and applying
// the target (or tenant-default) margin.
func (s *Service) RecalculateRecipeCosts(ctx context.Context, tenantID, recipeID uuid.UUID) error {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ID(recipeID)).
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

		// Sub-recipe ingredient: use its cost_per_portion instead of a raw item cost.
		if ing.SubRecipeID != nil && ing.Edges.SubRecipe != nil {
			sub := ing.Edges.SubRecipe
			if sub.CostPerPortion != nil {
				totalCost += effectiveQty * *sub.CostPerPortion
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

	// Determine margin: per-recipe takes priority over tenant config default.
	marginPct := r.TargetMarginPercent
	if marginPct == nil {
		cfg, cfgErr := s.client.TenantInventoryConfig.Query().
			Where(entconfig.TenantID(tenantID)).
			Only(ctx)
		if cfgErr == nil && cfg.DefaultTargetMarginPercent != nil {
			marginPct = cfg.DefaultTargetMarginPercent
		}
	}

	update := s.client.Recipe.UpdateOneID(recipeID).
		SetTotalCost(totalCost).
		SetCostPerPortion(costPerPortion)

	if marginPct != nil && *marginPct > 0 && *marginPct < 100 {
		suggestedPrice := costPerPortion / (1 - *marginPct/100)
		update = update.SetSuggestedPrice(suggestedPrice)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("update recipe costs: %w", err)
	}

	s.log.Info("recipe costs recalculated",
		zap.String("recipe_id", recipeID.String()),
		zap.Float64("total_cost", totalCost),
		zap.Float64("cost_per_portion", costPerPortion),
	)
	return nil
}
