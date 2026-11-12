package stock

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
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
	subs := s.cascadeSubRecipes(ctx, tx, tenantID, recipes)
	available := s.ingredientAvailability(ctx, tx, tenantID, warehouseID, recipes, subs)
	constrains := s.ingredientConstrainsFn(ctx, tenantID)
	for _, r := range recipes {
		// Non-depleting recipe items are never auto-86'd (they sell regardless of
		// tracked ingredient levels; the tenant counts stock manually). A production BOM's
		// item holds its own stock, so an ingredient running out does not take it off sale.
		if r.Kind != recipe.KindMenu || s.itemNonDepletingLazy(ctx, r.Edges.Item) {
			continue
		}
		if stockcalc.AllIngredientsAvailable(r, available, constrains, subs) {
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
// writeStockUpdated writes one inventory.stock.updated event for a movement of itm at warehouseID.
// It is the payload every stock movement shares; callers that also need the low-stock and recipe
// cascade use EmitStockChangeCascade, which calls this.
func (s *Service) writeStockUpdated(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, itm *ent.Item, warehouseID uuid.UUID, qtyBefore, qtyAfter, onHand, available float64, reason string) {
	s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.updated", map[string]any{
		"tenant_id":       tenantID.String(),
		"item_id":         itm.ID.String(),
		"sku":             itm.Sku,
		"warehouse_id":    warehouseID.String(),
		"outlet_id":       s.outletIDForWarehouse(ctx, tx, warehouseID), // "" = shared warehouse; lets consumers route per branch
		"quantity_before": qtyBefore,
		"quantity_change": qtyAfter - qtyBefore,
		"quantity_after":  qtyAfter,
		"reason":          reason,
		// One id per movement, written into the outbox payload so redeliveries carry the same id.
		// Treasury keys its KRA stock ledger on it; without it two identical movements (same
		// item, quantity and reason) collapsed into one.
		"adjustment_id": uuid.NewString(),
		"on_hand":       onHand,
		"available":     available,
	})
}

// isRecipeComponent reports whether a consumption line is an ingredient used by a recipe or BOM,
// rather than the sold item itself (whose finished SKU is its own SKU). Only components move stock
// that no fiscal document reports: a recipe's sale line holds no stock, so KRA learns about the
// ingredients leaving only from the consumption events.
func isRecipeComponent(sku, finishedSKU string) bool {
	return finishedSKU != "" && !strings.EqualFold(strings.TrimSpace(sku), strings.TrimSpace(finishedSKU))
}

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
	s.writeStockUpdated(ctx, tx, tenantID, itm, warehouseID, qtyBefore, qtyAfter, onHand, available, reason)
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
	subs := s.cascadeSubRecipes(ctx, tx, tenantID, recipes)
	available := s.ingredientAvailability(ctx, tx, tenantID, warehouseID, recipes, subs)
	constrains := s.ingredientConstrainsFn(ctx, tenantID)
	for _, r := range recipes {
		// Non-depleting recipe items and production BOM items were never 86'd by the
		// stock-out cascade, so there is nothing to unblock.
		if r.Kind != recipe.KindMenu || s.itemNonDepletingLazy(ctx, r.Edges.Item) {
			continue
		}
		if !stockcalc.AllIngredientsAvailable(r, available, constrains, subs) {
			continue
		}
		recipeItem := r.Edges.Item
		payload := map[string]any{
			"tenant_id":            tenantID.String(),
			"item_id":              recipeItem.ID.String(),
			"sku":                  recipeItem.Sku,
			"name":                 recipeItem.Name,
			"warehouse_id":         warehouseID.String(),
			"outlet_id":            outletID,
			"reason":               "ingredient_restocked",
			"affects_availability": true,
		}
		// Quantity-aware projection (STK-5): how many portions of this recipe its
		// ingredients can currently produce, so the catalog reflects a real stock level,
		// not just a sold-out boolean. An unconstrained recipe sends no figure at all, so
		// consumers keep it available without a quantity cap.
		if portions := stockcalc.ProduciblePortions(r, available, constrains, subs); portions != stockcalc.Unlimited {
			payload["available"] = portions
		}
		s.writeOutboxEvent(ctx, tx, tenantID, recipeItem.ID, "inventory", "stock.in", payload)
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

// recipesForIngredient loads every active recipe of the tenant that itemID gates and that
// produces a sellable item: the recipes listing it as a direct ingredient, and the parents
// that use any of those as a sub-recipe, up to stockcalc.MaxSubRecipeDepth levels up (a
// cocktail made from lemon juice is gated by lemons too). One query per level, each with the
// ingredients' items (and units) and the produced item eager-loaded.
func (s *Service) recipesForIngredient(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID) []*ent.Recipe {
	load := func(uses predicate.RecipeIngredient) ([]*ent.Recipe, error) {
		return tx.Recipe.Query().
			Where(
				recipe.TenantID(tenantID),
				recipe.IsActive(true),
				recipe.HasIngredientsWith(uses),
			).
			WithIngredients(func(q *ent.RecipeIngredientQuery) {
				q.WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() })
			}).
			WithItem().
			All(ctx)
	}
	found, err := load(recipeingredient.ItemID(itemID))
	if err != nil {
		s.log.Warn("cascade: query recipes using ingredient", zap.Error(err))
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(found))
	frontier := make([]uuid.UUID, 0, len(found))
	for _, r := range found {
		seen[r.ID] = struct{}{}
		frontier = append(frontier, r.ID)
	}
	for depth := 0; depth < stockcalc.MaxSubRecipeDepth && len(frontier) > 0; depth++ {
		parents, perr := load(recipeingredient.SubRecipeIDIn(frontier...))
		if perr != nil {
			s.log.Warn("cascade: query recipes using a sub-recipe", zap.Error(perr))
			break
		}
		next := make([]uuid.UUID, 0, len(parents))
		for _, p := range parents {
			if _, dup := seen[p.ID]; dup {
				continue
			}
			seen[p.ID] = struct{}{}
			next = append(next, p.ID)
			found = append(found, p)
		}
		frontier = next
	}
	out := found[:0]
	for _, r := range found {
		if r.Edges.Item != nil {
			out = append(out, r)
		}
	}
	return out
}

// cascadeSubRecipes loads the sub-recipes the cascade's recipes can expand into, so a parent
// recipe reads what its prep recipes can make rather than the prep item's empty balance.
func (s *Service) cascadeSubRecipes(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, recipes []*ent.Recipe) stockcalc.SubRecipes {
	subs, err := stockcalc.LoadSubRecipes(ctx, tx.Client(), tenantID, recipes)
	if err != nil {
		s.log.Warn("cascade: load sub-recipes", zap.Error(err))
	}
	return subs
}

// ingredientAvailability returns available stock at the warehouse for every ingredient of
// the given recipes and their sub-recipes, fetched in a single query. Missing rows are
// simply absent.
func (s *Service) ingredientAvailability(ctx context.Context, tx *ent.Tx, tenantID, warehouseID uuid.UUID, recipes []*ent.Recipe, subs stockcalc.SubRecipes) map[uuid.UUID]float64 {
	ids := stockcalc.IngredientItemIDs(recipes, subs)
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
