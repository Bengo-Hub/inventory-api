package stock

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/modules/stockcalc"
)

// cascadeIngredientStockOut publishes stock.out for any RECIPE-type items whose
// recipe can no longer be produced because itemID (an ingredient) hit zero.
// notification carries the tenant's alert-email opt-in block (computed once by the caller).
//
// Only runs for tenants that opted into auto_hide_on_stock_out; otherwise availability is
// manual-only and a depleted ingredient just keeps going negative.
// Best-effort: errors are logged, the parent transaction is never aborted.
func (s *Service) cascadeIngredientStockOut(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, notification map[string]any) {
	if !s.autoHideOnStockOut(ctx, tenantID) {
		return
	}
	recipes := s.recipesForIngredient(ctx, tx, tenantID, itemID)
	if len(recipes) == 0 {
		return
	}
	outletID := s.outletIDForWarehouse(ctx, tx, warehouseID)
	available := s.ingredientAvailability(ctx, tx, tenantID, warehouseID, recipes)
	constrains := s.ingredientConstrainsFn(ctx, tenantID)
	for _, r := range recipes {
		// Non-depleting recipe items are never auto-86'd (they sell regardless of
		// tracked ingredient levels — the tenant counts stock manually).
		if s.itemNonDepletingLazy(ctx, r.Edges.Item) {
			continue
		}
		if stockcalc.AllIngredientsAvailable(r, available, constrains) {
			continue
		}
		recipeItem := r.Edges.Item
		s.writeOutboxEvent(ctx, tx, tenantID, recipeItem.ID, "inventory", "stock.out", map[string]any{
			"tenant_id":            tenantID.String(),
			"item_id":              recipeItem.ID.String(),
			"sku":                  recipeItem.Sku,
			"name":                 recipeItem.Name,
			"available":            0,
			"warehouse_id":         warehouseID.String(),
			"outlet_id":            outletID,
			"reason":               "ingredient_depleted",
			"notification":         notification,
			"affects_availability": true,
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
// stock never re-enabled sold-out recipes or cleared low-stock alerts. Kept as a thin, direction-
// pinned wrapper around EmitStockChangeCascade for its existing (goods-receipt) call site.
func (s *Service) EmitStockInCascade(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, qtyBefore, qtyAfter float64) {
	s.EmitStockChangeCascade(ctx, tx, tenantID, itemID, warehouseID, qtyBefore, qtyAfter, "goods_receipt")
}

// EmitStockChangeCascade fires the same real-time downstream sync any direct InventoryBalance
// mutation made OUTSIDE the stock service needs — currently used by stock transfers (ship/
// receive/cancel move balances directly via their own adjustBalance) — so those moves get the
// exact same treatment as AdjustStock/RecordConsumption: publishes stock.updated (keeps
// ordering's quantity-aware catalog projection AND inventory-ui's live WebSocket push fresh —
// see StockNotifyEventsConsumer), rechecks the low-stock alert band, and — when the item crossed
// the zero boundary in EITHER direction — fires the matching ingredient-depletion/restock recipe
// cascade (stock.out/stock.in) so POS/ordering 86 or restore any recipe this item gates at that
// specific outlet (opt-in tenants only). Best-effort: errors are logged inside the helpers; the
// caller's transaction is never aborted.
func (s *Service) EmitStockChangeCascade(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, qtyBefore, qtyAfter float64, reason string) {
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
		"outlet_id":       s.outletIDForWarehouse(ctx, tx, warehouseID), // "" = shared warehouse; lets consumers route per branch
		"quantity_before": qtyBefore,
		"quantity_change": qtyAfter - qtyBefore,
		"quantity_after":  qtyAfter,
		"reason":          reason,
		"on_hand":         onHand,
		"available":       available,
	})
	if bal != nil {
		// checkAndPublishLowStock is the authoritative down-direction dispatcher: on a
		// transition into the out/low band it already publishes stock.out/stock.low AND
		// calls cascadeIngredientStockOut itself (band-transition-gated, idempotent). Calling
		// cascadeIngredientStockOut again here would double-publish for every affected recipe.
		s.checkAndPublishLowStock(ctx, tx, tenantID, itm, bal, warehouseID)
	}
	// The up-direction has no equivalent transition dispatcher — every other caller
	// (AdjustStock's restock branch, consumption reversal) fires this explicitly too.
	if qtyBefore <= 0 && qtyAfter > 0 {
		s.cascadeIngredientRestocked(ctx, tx, tenantID, itemID, warehouseID)
	}
}

// cascadeIngredientRestocked records the restock edge for the alert state machine and, for
// tenants that opted into auto_hide_on_stock_out, publishes stock.in for any RECIPE-type items
// whose recipe is now fully producible because itemID (an ingredient) was restocked.
// Best-effort: errors are logged, the parent transaction is never aborted.
func (s *Service) cascadeIngredientRestocked(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID) {
	outletID := s.outletIDForWarehouse(ctx, tx, warehouseID)
	var outletUUID *uuid.UUID
	if oid, perr := uuid.Parse(outletID); perr == nil {
		outletUUID = &oid
	}
	if bal, berr := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itemID), inventorybalance.WarehouseID(warehouseID)).
		First(ctx); berr == nil {
		// Skip when the alert state machine is already re-armed (checkAndPublishLowStock
		// may have just recorded the recovery edge for this same receipt).
		if s.lastStockLevelState(ctx, tx, tenantID, itemID, warehouseID) != stockBandOK {
			s.persistStockLevelEvent(ctx, tx, tenantID, itemID, warehouseID, outletUUID, "restocked", bal.Available, bal.ReorderLevel)
		}
	}
	// Manual-only availability: nothing was auto-86'd, so there is nothing to restore (and a
	// restock must never undo a staff member's own unavailable toggle).
	if !s.autoHideOnStockOut(ctx, tenantID) {
		return
	}
	recipes := s.recipesForIngredient(ctx, tx, tenantID, itemID)
	if len(recipes) == 0 {
		return
	}
	available := s.ingredientAvailability(ctx, tx, tenantID, warehouseID, recipes)
	constrains := s.ingredientConstrainsFn(ctx, tenantID)
	for _, r := range recipes {
		// Non-depleting recipe items were never 86'd, so there is nothing to unblock.
		if s.itemNonDepletingLazy(ctx, r.Edges.Item) {
			continue
		}
		if !stockcalc.AllIngredientsAvailable(r, available, constrains) {
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
			// Quantity-aware projection (STK-5): how many portions of this recipe its
			// ingredients can currently produce, so the catalog reflects a real stock
			// level, not just a sold-out boolean.
			"available":            stockcalc.ProduciblePortions(r, available, constrains),
			"affects_availability": true,
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

// recipesForIngredient loads, in one query, every active recipe of the tenant that lists
// itemID as a direct ingredient and produces a sellable item, with each ingredient's item
// (and unit) and the produced item eager-loaded. Replaces the old per-recipe lookups.
func (s *Service) recipesForIngredient(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID) []*ent.Recipe {
	rs, err := tx.Recipe.Query().
		Where(
			recipe.TenantID(tenantID),
			recipe.IsActive(true),
			recipe.ItemIDNotNil(),
			recipe.HasIngredientsWith(recipeingredient.ItemID(itemID)),
		).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() })
		}).
		WithItem().
		All(ctx)
	if err != nil {
		s.log.Warn("cascade: query recipes using ingredient", zap.Error(err))
		return nil
	}
	out := rs[:0]
	for _, r := range rs {
		if r.Edges.Item != nil {
			out = append(out, r)
		}
	}
	return out
}

// ingredientAvailability returns available stock at the warehouse for every ingredient of
// the given recipes, fetched in a single query. Missing rows are simply absent.
func (s *Service) ingredientAvailability(ctx context.Context, tx *ent.Tx, tenantID, warehouseID uuid.UUID, recipes []*ent.Recipe) map[uuid.UUID]float64 {
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for _, r := range recipes {
		for _, ing := range r.Edges.Ingredients {
			if _, ok := seen[ing.ItemID]; !ok {
				seen[ing.ItemID] = struct{}{}
				ids = append(ids, ing.ItemID)
			}
		}
	}
	available := make(map[uuid.UUID]float64, len(ids))
	if len(ids) == 0 {
		return available
	}
	bals, err := tx.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.WarehouseID(warehouseID),
			inventorybalance.ItemIDIn(ids...),
		).
		All(ctx)
	if err != nil {
		s.log.Warn("cascade: query ingredient balances", zap.Error(err))
		return available
	}
	for _, b := range bals {
		available[b.ItemID] = b.Available
	}
	return available
}

// isCompositeReservationLine reports whether a reservation line is a recipe (menu-item)
// summary whose stock is held by its exploded ingredient lines. Release and consume must
// skip it: moving the recipe item's own balance for it double-counts the sale and drives the
// recipe item negative. Reservations written before the Composite tag existed are recognised
// by the item being a RECIPE with an active, non-empty BOM (exactly when CreateReservation
// exploded it). itm must be the line's item.
func (s *Service) isCompositeReservationLine(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, ri entschema.ReservedItemJSON, itm *ent.Item) bool {
	if ri.Composite {
		return true
	}
	if itm == nil || itm.Type != item.TypeRECIPE {
		return false
	}
	exists, err := tx.Recipe.Query().
		Where(
			recipe.TenantID(tenantID),
			recipe.Sku(itm.Sku),
			recipe.IsActive(true),
			recipe.HasIngredients(),
		).
		Exist(ctx)
	return err == nil && exists
}

// ingredientConstrainsFn returns the predicate deciding whether an ingredient participates
// in availability gating: non-depleting ingredient items never constrain (their balances are
// not maintained by sales). The tenant config is resolved once for the whole cascade.
func (s *Service) ingredientConstrainsFn(ctx context.Context, tenantID uuid.UUID) func(*ent.Item) bool {
	cfg := s.tenantConfig(ctx, tenantID)
	return func(itm *ent.Item) bool { return !isNonDepleting(itm, cfg) }
}
