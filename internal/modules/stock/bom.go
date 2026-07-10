package stock

import (
	"context"
	"math"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	enttenantcfg "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// maxSubRecipeDepth caps sub-recipe backflush recursion (cycle guard): a menu recipe
// may explode into a prep recipe (lemon juice) whose own ingredients are raw items.
const maxSubRecipeDepth = 3

// explodedIngredient is a single resolved stock deduction produced by BOM explosion.
// Quantity is expressed in the ingredient item's STOCK (base) unit — recipe-line units
// (ml, g, tot pours against a bottle) are converted before any balance is touched.
type explodedIngredient struct {
	SKU      string
	Quantity float64 // in stock units; 0 when UnitMismatch
	// UnitMismatch marks a line whose recipe unit could not be converted to the item's
	// stock unit (cross-dimension with no unit_content declared). NO stock is deducted —
	// deducting a raw number would corrupt the balance — but the line is still recorded
	// on the Consumption for visibility in variance reports.
	UnitMismatch bool
	// Theoretical marks a line for a non-depleting item: recorded for AvT/food-cost
	// reporting, but the balance is never decremented.
	Theoretical bool
	// RequestedQty/RequestedUOM preserve the original recipe-line expression when it
	// differs from the deducted stock quantity (post-conversion audit trail).
	RequestedQty float64
	RequestedUOM string
	// RecipeID/RecipeSKU identify the TOP-level menu recipe this ingredient line was
	// exploded for — propagated unchanged through sub-recipe backflush recursion, so a
	// prep-recipe ingredient (lemon juice's raw lemons) still attributes to the menu item
	// (a cocktail), not the intermediate prep. Zero/empty for a line resolved outside BOM
	// explosion (direct-sale item with no recipe).
	RecipeID  uuid.UUID
	RecipeSKU string
	// FinishedItemSKU is the sale-line SKU this line was consumed for — set by the caller
	// (RecordConsumption) uniformly across BOM'd, direct, and modifier-derived lines, since
	// only the caller knows the sale-line context. Equals RecipeSKU for BOM'd lines.
	FinishedItemSKU string
}

// round4 rounds to 4 decimal places to avoid floating-point drift accumulating on balances.
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// expenseBearingReason reports whether a downward stock adjustment must post an
// operating-expense/wastage journal entry in treasury: internal consumption of
// floor-stock consumables (serviettes, tissues) and stock written off as damaged,
// expired or shrunk. Counting/transfer/correction reasons move value, not expense.
func expenseBearingReason(r stockadjustment.Reason) bool {
	switch r {
	case stockadjustment.ReasonInternalConsumption,
		stockadjustment.ReasonDamaged,
		stockadjustment.ReasonExpired,
		stockadjustment.ReasonShrinkage:
		return true
	}
	return false
}

// tenantConfig loads the tenant's inventory config row (nil when none exists).
func (s *Service) tenantConfig(ctx context.Context, tenantID uuid.UUID) *ent.TenantInventoryConfig {
	cfg, err := s.client.TenantInventoryConfig.Query().
		Where(enttenantcfg.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil
	}
	return cfg
}

// isNonDepleting reports whether sales must NOT decrement this item's stock.
// Explicit per-item mode always wins; items left on "default" follow the tenant's
// recipe_items_non_depleting_default policy — but only RECIPE-type items (goods,
// ingredients and bottles keep depleting so easy-to-track stock stays accurate).
func isNonDepleting(itm *ent.Item, cfg *ent.TenantInventoryConfig) bool {
	if itm == nil {
		return false
	}
	switch itm.StockTrackingMode {
	case item.StockTrackingModeNonDepleting:
		return true
	case item.StockTrackingModeTracked:
		return false
	}
	return itm.Type == item.TypeRECIPE && cfg != nil && cfg.RecipeItemsNonDepletingDefault
}

// itemNonDepletingLazy is isNonDepleting for call sites that don't already hold the
// tenant config: it only pays the config query for RECIPE-type items left on "default"
// (explicit modes and non-recipe items resolve without a lookup).
func (s *Service) itemNonDepletingLazy(ctx context.Context, itm *ent.Item) bool {
	if itm == nil {
		return false
	}
	switch itm.StockTrackingMode {
	case item.StockTrackingModeNonDepleting:
		return true
	case item.StockTrackingModeTracked:
		return false
	}
	if itm.Type != item.TypeRECIPE {
		return false
	}
	return isNonDepleting(itm, s.tenantConfig(ctx, itm.TenantID))
}

// ConvertToStockUnit converts a quantity expressed in a recipe-line/sale unit into the
// item's stock (base) unit. Resolution order:
//  1. same-dimension unit conversion (ml→l, g→kg, …) via the built-in units table;
//  2. content-per-unit bridge for count-stocked packaged goods: a 30 ml line against a
//     750 ml-per-piece bottle deducts 30/750 = 0.04 pieces (cumulative tots deplete
//     whole bottles exactly);
//  3. unknown/absent units pass through unchanged (legacy rows written pre-normalised);
//  4. cross-dimension with no bridge → ok=false: the caller must NOT deduct raw.
func ConvertToStockUnit(itm *ent.Item, qty float64, fromUOM string) (float64, bool) {
	from := units.NormalizeUnit(fromUOM)
	stockUnit := ""
	if itm != nil && itm.Edges.Units != nil {
		stockUnit = units.NormalizeUnit(itm.Edges.Units.Abbreviation)
	}
	// Without both units we cannot judge — preserve the historical raw-passthrough so
	// pre-normalised rows (written by the composite flow in base units) keep working.
	if from == "" || stockUnit == "" || from == stockUnit {
		return qty, true
	}
	if converted, ok := units.Convert(qty, from, stockUnit); ok {
		return converted, true
	}
	// Content-per-unit bridge (pieces ↔ ml/g) for fixed-content packaged goods.
	if itm.UnitContentQty != nil && *itm.UnitContentQty > 0 && itm.UnitContentUom != "" {
		if inContent, ok := units.Convert(qty, from, itm.UnitContentUom); ok {
			return inContent / *itm.UnitContentQty, true
		}
	}
	return qty, false
}

// explodeBOM resolves a menu-item SKU to its raw-ingredient stock deductions using the
// recipe/BOM table. Returns (nil, false) when the SKU has no active recipe or the recipe
// has no ingredients — the caller consumes the SKU directly.
//
// Ingredient quantities include the line's waste factor (matching recipe costing, so
// theoretical usage and money agree) and are converted into each ingredient's stock
// unit (see ConvertToStockUnit). An ingredient line carrying a sub_recipe reference
// deducts the prepared item's stock when any exists at the warehouse; otherwise it
// backflushes: the sub-recipe's own BOM is exploded (depth-capped) so tenants that
// don't record prep batches still deplete raw materials.
func (s *Service) explodeBOM(ctx context.Context, tenantID, warehouseID uuid.UUID, sku string, portionsRequested float64) ([]explodedIngredient, bool) {
	return s.explodeBOMDepth(ctx, tenantID, warehouseID, sku, portionsRequested, 0, uuid.Nil, "")
}

// topRecipeID/topRecipeSKU are uuid.Nil/"" on the initial (depth-0) call — the function
// establishes them from the recipe it resolves and passes them down unchanged through any
// sub-recipe backflush recursion, so every returned ingredient attributes to the ORIGINAL
// menu recipe, not an intermediate prep recipe.
func (s *Service) explodeBOMDepth(ctx context.Context, tenantID, warehouseID uuid.UUID, sku string, portionsRequested float64, depth int, topRecipeID uuid.UUID, topRecipeSKU string) ([]explodedIngredient, bool) {
	// A variant SKU has no recipe of its own — it shares the parent item's BOM. Resolve
	// to the parent's SKU so the recipe lookup hits (a real-item SKU passes through).
	sku = s.resolveStockSKU(ctx, tenantID, sku)
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(sku), recipe.IsActive(true)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() }).
				WithSubRecipe()
		}).
		Only(ctx)
	if err != nil || len(r.Edges.Ingredients) == 0 {
		return nil, false
	}

	if topRecipeID == uuid.Nil {
		topRecipeID = r.ID
		topRecipeSKU = r.Sku
	}

	outputQty := r.OutputQty
	if outputQty <= 0 {
		outputQty = 1
	}

	ingredients := make([]explodedIngredient, 0, len(r.Edges.Ingredients))
	for _, ing := range r.Edges.Ingredients {
		// Scale by (portions / outputQty), keeping fractions, and include the line's
		// waste factor so stock depletion matches the costed theoretical usage.
		qty := ing.Quantity * (1 + ing.WastePercent/100) / outputQty * portionsRequested
		if qty <= 0 {
			continue
		}

		ingItem := ing.Edges.Item

		// Prep/sub-recipe line: prefer the prepared item's own stock (recorded via
		// production batches or bought ready-made); backflush its BOM when unstocked.
		if ing.SubRecipeID != nil && ing.Edges.SubRecipe != nil && depth < maxSubRecipeDepth {
			if !s.hasPositiveBalance(ctx, tenantID, ing.ItemID, warehouseID) {
				// Convert the line into the prepared item's base unit first: the
				// sub-recipe's output_qty is expressed in that same unit by convention
				// (a 480 ml lemon-juice batch has output_qty 480, unit ml).
				subQty, ok := ConvertToStockUnit(ingItem, qty, ing.UnitOfMeasure)
				if ok {
					if subIngs, isBOM := s.explodeBOMDepth(ctx, tenantID, warehouseID, ing.Edges.SubRecipe.Sku, subQty, depth+1, topRecipeID, topRecipeSKU); isBOM {
						ingredients = append(ingredients, subIngs...)
						continue
					}
				}
			}
		}

		converted, ok := ConvertToStockUnit(ingItem, qty, ing.UnitOfMeasure)
		if !ok {
			s.log.Warn("BOM line unit mismatch — NOT deducted (declare unit_content on the item or use a prepared ingredient)",
				zap.String("recipe_sku", sku),
				zap.String("ingredient_sku", ing.ItemSku),
				zap.Float64("quantity", qty),
				zap.String("line_uom", ing.UnitOfMeasure),
			)
			ingredients = append(ingredients, explodedIngredient{
				SKU:          ing.ItemSku,
				Quantity:     0,
				UnitMismatch: true,
				RequestedQty: round4(qty),
				RequestedUOM: ing.UnitOfMeasure,
				RecipeID:     topRecipeID,
				RecipeSKU:    topRecipeSKU,
			})
			continue
		}

		line := explodedIngredient{SKU: ing.ItemSku, Quantity: round4(converted), RecipeID: topRecipeID, RecipeSKU: topRecipeSKU}
		if converted != qty {
			line.RequestedQty = round4(qty)
			line.RequestedUOM = ing.UnitOfMeasure
		}
		ingredients = append(ingredients, line)
	}
	return ingredients, true
}

// hasPositiveBalance reports whether the item has any available stock at the warehouse.
func (s *Service) hasPositiveBalance(ctx context.Context, tenantID, itemID, warehouseID uuid.UUID) bool {
	bal, err := s.client.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itemID),
			inventorybalance.WarehouseID(warehouseID),
		).
		First(ctx)
	return err == nil && bal.Available > 0
}
