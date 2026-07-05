package recipes

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// defaultFoodCostTarget is the maximum healthy food-cost percentage (35%).
const defaultFoodCostTarget = 0.35

// foodCostStatus returns a human-readable status for the computed food-cost %.
func foodCostStatus(fcPct, target float64) string {
	if fcPct >= 1.0 {
		return "LOSS - cost >= price"
	}
	if fcPct > target {
		return "OK - above target FC%"
	}
	return "OK - healthy"
}

// RecalculateRecipeCosts computes and persists total_cost, cost_per_portion, and suggested_price
// for a recipe by summing ingredient cost_prices (and sub-recipe cost_per_portion) and applying
// the target (or tenant-default) margin.
func (s *Service) RecalculateRecipeCosts(ctx context.Context, tenantID, recipeID uuid.UUID) error {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ID(recipeID)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() }).WithSubRecipe()
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
		// The recipe line is written in whatever unit the dish actually consumes (e.g. grams,
		// millilitres), while the ingredient's cost_price is per its NATURAL stocking/purchase unit
		// (e.g. per kg, per litre) — staff shouldn't have to pre-convert a market price into a
		// fractional per-gram figure before linking it into a recipe. The composite create-flow
		// (inventory_composite.go) already converts a line's quantity into the ingredient's base
		// unit at write time using units.Convert; do the same conversion here at read time so a
		// recalculation is correct even for rows written some other way (bulk import, direct edit).
		// Unrecognized/mismatched pairs (count units, or a family like "cup"/"shot" with no fixed
		// real-world size) fall back to a direct multiply, same as the previous behavior.
		qty := effectiveQty
		if ing.Edges.Item.Edges.Units != nil {
			if converted, ok := units.Convert(effectiveQty, ing.UnitOfMeasure, ing.Edges.Item.Edges.Units.Abbreviation); ok {
				qty = converted
			}
		}
		totalCost += qty * *ing.Edges.Item.CostPrice
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

	// Compute food_cost_pct and status when selling_price is set.
	if r.SellingPrice != nil && *r.SellingPrice > 0 {
		fcPct := costPerPortion / *r.SellingPrice
		update = update.SetFoodCostPct(fcPct).SetStatus(foodCostStatus(fcPct, defaultFoodCostTarget))
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
