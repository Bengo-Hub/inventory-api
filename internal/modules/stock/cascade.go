package stock

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
)

// cascadeIngredientStockOut publishes stock.out for any RECIPE-type items whose
// recipe can no longer be produced because itemID (an ingredient) hit zero.
// Best-effort: errors are logged, the parent transaction is never aborted.
func (s *Service) cascadeIngredientStockOut(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID) {
	outletID := s.outletIDForWarehouse(ctx, tx, warehouseID)
	for _, recipeID := range s.recipesUsingIngredient(ctx, tx, itemID) {
		r, err := tx.Recipe.Query().
			Where(recipe.ID(recipeID), recipe.TenantID(tenantID), recipe.IsActive(true)).
			WithIngredients().
			WithItem().
			Only(ctx)
		if err != nil || r.ItemID == nil || r.Edges.Item == nil {
			continue
		}
		if s.allIngredientsAvailable(ctx, tx, tenantID, r.Edges.Ingredients, warehouseID) {
			continue
		}
		recipeItem := r.Edges.Item
		s.writeOutboxEvent(ctx, tx, tenantID, recipeItem.ID, "inventory", "stock.out", map[string]any{
			"tenant_id":    tenantID.String(),
			"item_id":      recipeItem.ID.String(),
			"sku":          recipeItem.Sku,
			"name":         recipeItem.Name,
			"available":    0,
			"warehouse_id": warehouseID.String(),
			"outlet_id":    outletID,
			"reason":       "ingredient_depleted",
			"notification": map[string]any{"target": "staff"},
		})
		s.log.Info("cascade stock.out: recipe item blocked",
			zap.String("recipe_sku", recipeItem.Sku),
			zap.String("depleted_ingredient_id", itemID.String()),
		)
	}
}

// EmitStockInCascade fires the same downstream cascade as an upward stock adjustment for a
// stock-in that was applied OUTSIDE the stock service (e.g. a goods-receipt line that wrote the
// InventoryBalance directly): it publishes stock.updated, rechecks low-stock, and — when the item
// crossed back above zero — re-enables any recipes its depletion had gated. Without this, received
// stock never re-enabled sold-out recipes or cleared low-stock alerts. Best-effort: errors are
// logged inside the helpers; the caller's transaction is never aborted.
func (s *Service) EmitStockInCascade(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, qtyBefore, qtyAfter float64) {
	itm, err := tx.Item.Get(ctx, itemID)
	if err != nil {
		return
	}
	bal, _ := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itemID), inventorybalance.WarehouseID(warehouseID)).
		First(ctx)
	onHand, available := qtyAfter, qtyAfter
	if bal != nil {
		onHand, available = bal.OnHand, bal.Available
	}
	s.writeOutboxEvent(ctx, tx, tenantID, itemID, "inventory", "stock.updated", map[string]any{
		"tenant_id":       tenantID.String(),
		"item_id":         itemID.String(),
		"sku":             itm.Sku,
		"warehouse_id":    warehouseID.String(),
		"quantity_before": qtyBefore,
		"quantity_change": qtyAfter - qtyBefore,
		"quantity_after":  qtyAfter,
		"reason":          "goods_receipt",
		"on_hand":         onHand,
		"available":       available,
	})
	if bal != nil {
		s.checkAndPublishLowStock(ctx, tx, tenantID, itm, bal, warehouseID)
	}
	if qtyBefore <= 0 && qtyAfter > 0 {
		s.cascadeIngredientRestocked(ctx, tx, tenantID, itemID, warehouseID)
	}
}

// cascadeIngredientRestocked publishes stock.in for any RECIPE-type items whose
// recipe is now fully producible because itemID (an ingredient) was restocked.
// Best-effort: errors are logged, the parent transaction is never aborted.
func (s *Service) cascadeIngredientRestocked(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID) {
	outletID := s.outletIDForWarehouse(ctx, tx, warehouseID)
	for _, recipeID := range s.recipesUsingIngredient(ctx, tx, itemID) {
		r, err := tx.Recipe.Query().
			Where(recipe.ID(recipeID), recipe.TenantID(tenantID), recipe.IsActive(true)).
			WithIngredients().
			WithItem().
			Only(ctx)
		if err != nil || r.ItemID == nil || r.Edges.Item == nil {
			continue
		}
		if !s.allIngredientsAvailable(ctx, tx, tenantID, r.Edges.Ingredients, warehouseID) {
			continue
		}
		recipeItem := r.Edges.Item
		s.writeOutboxEvent(ctx, tx, tenantID, recipeItem.ID, "inventory", "stock.in", map[string]any{
			"tenant_id":    tenantID.String(),
			"item_id":      recipeItem.ID.String(),
			"sku":          recipeItem.Sku,
			"name":         recipeItem.Name,
			"warehouse_id": warehouseID.String(),
			"outlet_id":    outletID,
			"reason":       "ingredient_restocked",
		})
		s.log.Info("cascade stock.in: recipe item unblocked",
			zap.String("recipe_sku", recipeItem.Sku),
			zap.String("restocked_ingredient_id", itemID.String()),
		)
	}
}

// outletIDForWarehouse resolves the outlet a warehouse serves (warehouses.outlet_id),
// so cascade events can carry the outlet whose catalog override must be toggled.
// Returns "" when the warehouse is shared/HQ (outlet_id nil) or cannot be loaded;
// consumers treat "" as "fall back to sku-wide update".
func (s *Service) outletIDForWarehouse(ctx context.Context, tx *ent.Tx, warehouseID uuid.UUID) string {
	wh, err := tx.Warehouse.Get(ctx, warehouseID)
	if err != nil || wh == nil || wh.OutletID == nil {
		return ""
	}
	return wh.OutletID.String()
}

// recipesUsingIngredient returns distinct recipe IDs that list itemID as a direct ingredient.
func (s *Service) recipesUsingIngredient(ctx context.Context, tx *ent.Tx, itemID uuid.UUID) []uuid.UUID {
	ings, err := tx.RecipeIngredient.Query().
		Where(recipeingredient.ItemID(itemID)).
		All(ctx)
	if err != nil {
		s.log.Warn("cascade: query recipe ingredients", zap.Error(err))
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(ings))
	ids := make([]uuid.UUID, 0, len(ings))
	for _, ing := range ings {
		if _, ok := seen[ing.RecipeID]; !ok {
			seen[ing.RecipeID] = struct{}{}
			ids = append(ids, ing.RecipeID)
		}
	}
	return ids
}

// allIngredientsAvailable returns true only if every ingredient in the recipe
// has a balance row with available > 0 in the given warehouse.
func (s *Service) allIngredientsAvailable(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, ingredients []*ent.RecipeIngredient, warehouseID uuid.UUID) bool {
	for _, ing := range ingredients {
		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(ing.ItemID),
				inventorybalance.WarehouseID(warehouseID),
			).
			First(ctx)
		if err != nil || bal.Available <= 0 {
			return false
		}
	}
	return true
}
