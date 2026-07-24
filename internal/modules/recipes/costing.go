package recipes

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/modules/stock"
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

// SetSellingPriceByItem updates the selling price of the active recipe linked to an item.
// RECIPE items are priced by their recipe at the POS (the recipe price outranks raw tier
// rows), so a catalog price correction must land here or it silently never takes effect.
// Recomputes food_cost_pct/status from the stored cost_per_portion. Returns false when
// the item has no active recipe (nothing to update — not an error).
func (s *Service) SetSellingPriceByItem(ctx context.Context, tenantID, itemID uuid.UUID, price float64) (bool, error) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ItemID(itemID), recipe.IsActive(true)).
		First(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recipes: load recipe for item %s: %w", itemID, err)
	}
	upd := s.client.Recipe.UpdateOneID(r.ID).SetSellingPrice(price)
	if r.CostPerPortion != nil && price > 0 {
		fcPct := *r.CostPerPortion / price
		upd = upd.SetFoodCostPct(fcPct).SetStatus(foodCostStatus(fcPct, defaultFoodCostTarget))
	}
	if _, err := upd.Save(ctx); err != nil {
		return false, fmt.Errorf("recipes: set selling price for recipe %s: %w", r.ID, err)
	}
	return true, nil
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
		// cost_per_portion is per OUTPUT unit — the prepared item's stock unit by
		// convention (per ml for a 1000 ml prep batch, per pot/portion for a menu
		// recipe) — so the line qty must be converted into that unit exactly like the
		// deduction path does, content-per-unit bridge included: a 30 ml line against
		// a 300 ml-per-pot Black Tea costs 0.1 × its per-pot cost, not 30 ×.
		if ing.SubRecipeID != nil && ing.Edges.SubRecipe != nil {
			sub := ing.Edges.SubRecipe
			if sub.CostPerPortion != nil {
				qty := effectiveQty
				if ing.Edges.Item != nil {
					if converted, ok := stock.ConvertToStockUnit(ing.Edges.Item, effectiveQty, ing.UnitOfMeasure); ok {
						qty = converted
					}
				}
				totalCost += qty * *sub.CostPerPortion
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
		// fractional per-gram figure before linking it into a recipe. Convert the same way the
		// stock-deduction path does (stock.ConvertToStockUnit), including the content-per-unit
		// bridge: a 30 ml tot against a 750 ml-per-piece bottle costs 0.04 × the per-piece cost,
		// so tot margins are right. Unrecognized/unbridgeable pairs fall back to a direct
		// multiply, same as the previous behavior (the write-time guard prevents new ones).
		qty := effectiveQty
		if converted, ok := stock.ConvertToStockUnit(ing.Edges.Item, effectiveQty, ing.UnitOfMeasure); ok {
			qty = converted
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

	// Write the recomputed cost through onto the recipe's owning Item.CostPrice — every existing
	// COGS/profit consumer (treasury's sale-time COGS journal, P&L cost-of-goods, the reversal
	// tool's cost lookup, the profitability report) reads ONLY Item.CostPrice, never
	// Recipe.CostPerPortion, so without this a recipe's real ingredient cost never reached any of
	// them: COGS silently posted 0 for a RECIPE sale, and "profit" was actually gross revenue.
	// Best-effort — a failure here must never block the recipe cost recompute itself.
	if s.items != nil && r.ItemID != nil {
		if perr := s.items.SetCostPriceAndPublish(ctx, tenantID, *r.ItemID, costPerPortion); perr != nil {
			s.log.Warn("recipe cost write-through to item.cost_price failed (non-fatal)",
				zap.String("recipe_id", recipeID.String()), zap.String("item_id", r.ItemID.String()), zap.Error(perr))
		}
	}

	s.log.Info("recipe costs recalculated",
		zap.String("recipe_id", recipeID.String()),
		zap.Float64("total_cost", totalCost),
		zap.Float64("cost_per_portion", costPerPortion),
	)
	return nil
}
