package items

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/modules/stockcalc"
	"github.com/bengobox/inventory-service/internal/modules/tenantconfig"
)

// holdsOwnStock reports whether an item keeps a balance of its own that can run out: goods,
// ingredients and equipment, and a RECIPE item whose active recipe is a production BOM
// (manufactured finished goods made in batches). Services, vouchers and menu recipes never do.
// stockBearingBalance is the same rule as a query predicate.
func holdsOwnStock(t item.Type, productionBOM bool) bool {
	return IsStockTracked(t) || (t == item.TypeRECIPE && productionBOM)
}

// stockBearingBalance limits balance rows to items that hold their own stock (holdsOwnStock),
// so a service's or a menu recipe's stray zero row is never counted as out of stock.
func stockBearingBalance() predicate.InventoryBalance {
	return inventorybalance.HasItemWith(item.Or(
		item.TypeIn(item.TypeGOODS, item.TypeINGREDIENT, item.TypeEQUIPMENT),
		item.And(
			item.TypeEQ(item.TypeRECIPE),
			item.HasProducedByRecipeWith(recipe.IsActive(true), recipe.KindNEQ(recipe.KindMenu)),
		),
	))
}

// recipeStock is what a RECIPE item's active recipe says about its stock.
type recipeStock struct {
	// production is true for a production BOM (kind other than menu): the item holds its own
	// balance and reads it like goods.
	production bool
	// hasBOM is true for a menu recipe with at least one ingredient line.
	hasBOM bool
	// portions is what the ingredients can make (menu recipes with lines only);
	// stockcalc.Unlimited when no line constrains stock.
	portions float64
	uom      string
}

// recipeStockForItems resolves the active recipe of each RECIPE item (matched by item_id, else
// by SKU, the older link) and, for menu recipes with lines, the portions their ingredients and
// sub-recipes can make over the warehouse scope (nil means every warehouse). Items with no
// active recipe are absent. The number of queries does not grow with the number of items: the
// recipes, one per sub-recipe level, then the balances.
func (s *Service) recipeStockForItems(ctx context.Context, tenantID uuid.UUID, recipeItems []*ent.Item, scope map[uuid.UUID]struct{}) (map[uuid.UUID]recipeStock, error) {
	out := make(map[uuid.UUID]recipeStock, len(recipeItems))
	if len(recipeItems) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(recipeItems))
	skus := make([]string, 0, len(recipeItems))
	byID := make(map[uuid.UUID]*ent.Item, len(recipeItems))
	bySKU := make(map[string]*ent.Item, len(recipeItems))
	for _, itm := range recipeItems {
		ids = append(ids, itm.ID)
		skus = append(skus, itm.Sku)
		byID[itm.ID] = itm
		bySKU[itm.Sku] = itm
	}
	recs, err := s.client.Recipe.Query().
		Where(
			recipe.TenantID(tenantID),
			recipe.IsActive(true),
			recipe.Or(recipe.ItemIDIn(ids...), recipe.SkuIn(skus...)),
		).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() })
		}).
		All(ctx)
	if err != nil {
		return out, fmt.Errorf("items: load recipes: %w", err)
	}

	// An item_id link wins over a SKU match.
	recipeOf := make(map[uuid.UUID]*ent.Recipe, len(recs))
	for _, r := range recs {
		if r.ItemID != nil {
			if _, ok := byID[*r.ItemID]; ok {
				recipeOf[*r.ItemID] = r
			}
		}
	}
	for _, r := range recs {
		if itm, ok := bySKU[r.Sku]; ok {
			if _, linked := recipeOf[itm.ID]; !linked {
				recipeOf[itm.ID] = r
			}
		}
	}

	menu := make([]*ent.Recipe, 0, len(recipeOf))
	for itemID, r := range recipeOf {
		rs := recipeStock{production: r.Kind != recipe.KindMenu, uom: r.UnitOfMeasure}
		rs.hasBOM = !rs.production && len(r.Edges.Ingredients) > 0
		if rs.hasBOM {
			menu = append(menu, r)
		}
		out[itemID] = rs
	}
	if len(menu) == 0 {
		return out, nil
	}

	subs, err := stockcalc.LoadSubRecipes(ctx, s.client, tenantID, menu)
	if err != nil {
		return out, fmt.Errorf("items: load sub-recipes: %w", err)
	}
	available := make(map[uuid.UUID]float64)
	if ingIDs := stockcalc.IngredientItemIDs(menu, subs); len(ingIDs) > 0 {
		bals, berr := s.client.InventoryBalance.Query().
			Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.ItemIDIn(ingIDs...)).
			All(ctx)
		if berr != nil {
			return out, fmt.Errorf("items: query ingredient balances: %w", berr)
		}
		// Sum across warehouses: an item stocked in several locations has one row each.
		for _, b := range bals {
			if scope != nil {
				if _, ok := scope[b.WarehouseID]; !ok {
					continue
				}
			}
			available[b.ItemID] += b.Available
		}
	}
	cfg := tenantconfig.Get(ctx, s.client, tenantID)
	constrains := func(ingItem *ent.Item) bool { return !stockcalc.NonDepleting(ingItem, cfg) }
	for itemID, r := range recipeOf {
		rs := out[itemID]
		if !rs.hasBOM {
			continue
		}
		// Floors at 0 when a constraining ingredient is negative (a legitimate oversold
		// state, see [[oversell-negative-stock-settlement]]).
		rs.portions = stockcalc.ProduciblePortions(r, available, constrains, subs)
		out[itemID] = rs
	}
	return out, nil
}

// balanceTotal is an item's own balance summed over its warehouse rows.
type balanceTotal struct {
	onHand, available, reserved float64
	// warehouseID is set only when the item sits in exactly one warehouse.
	warehouseID uuid.UUID
	rows        int
	uom         string
	updatedAt   time.Time
}

func (b *balanceTotal) add(row *ent.InventoryBalance) {
	b.onHand += row.OnHand
	b.available += row.Available
	b.reserved += row.Reserved
	b.rows++
	if b.rows == 1 {
		b.warehouseID = row.WarehouseID
		b.uom = row.UnitOfMeasure
	} else {
		b.warehouseID = uuid.Nil
	}
	if row.UpdatedAt.After(b.updatedAt) {
		b.updatedAt = row.UpdatedAt
	}
}

// availabilityFor builds one item's availability from its type, its recipe (RECIPE items, nil
// when it has none) and its own balance (nil when it has no row). The rules:
//   - SERVICE and VOUCHER track no stock;
//   - a RECIPE with a production BOM reads its own balance like goods;
//   - a menu RECIPE with lines reads the portions its ingredients can make, and tracks no stock
//     when nothing constrains it;
//   - a menu RECIPE with no lines, or no recipe at all, tracks no stock.
//
// An item that tracks no stock reports 0 with TracksStock false: callers must read that as
// always available, never as out of stock.
func availabilityFor(itm *ent.Item, rs *recipeStock, own *balanceTotal) StockAvailability {
	out := StockAvailability{
		ItemID:    itm.ID,
		SKU:       itm.Sku,
		UpdatedAt: itm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if itm.Type == item.TypeRECIPE && (rs == nil || !rs.production) {
		if rs != nil {
			out.UnitOfMeasure = rs.uom
		}
		if rs == nil || !rs.hasBOM || rs.portions == stockcalc.Unlimited {
			return out
		}
		out.TracksStock = true
		out.OnHand, out.Available = rs.portions, rs.portions
		return out
	}
	if !holdsOwnStock(itm.Type, rs != nil && rs.production) {
		return out
	}
	out.TracksStock = true
	if own != nil {
		out.WarehouseID = own.warehouseID
		out.OnHand = own.onHand
		out.Available = own.available
		out.Reserved = own.reserved
		out.UnitOfMeasure = own.uom
		if !own.updatedAt.IsZero() {
			out.UpdatedAt = own.updatedAt.Format("2006-01-02T15:04:05Z")
		}
	}
	return out
}

// availabilityForItems resolves availability for a batch of items in a fixed number of
// queries: their own balances, then the recipes of the RECIPE items with their ingredients'
// balances. Results follow the order of itms.
func (s *Service) availabilityForItems(ctx context.Context, tenantID uuid.UUID, itms []*ent.Item) ([]StockAvailability, error) {
	if len(itms) == 0 {
		return []StockAvailability{}, nil
	}
	ids := make([]uuid.UUID, len(itms))
	recipeItems := make([]*ent.Item, 0)
	for i, itm := range itms {
		ids[i] = itm.ID
		if itm.Type == item.TypeRECIPE {
			recipeItems = append(recipeItems, itm)
		}
	}
	balances, err := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemIDIn(ids...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query balances: %w", err)
	}
	own := make(map[uuid.UUID]*balanceTotal, len(balances))
	for _, b := range balances {
		t := own[b.ItemID]
		if t == nil {
			t = &balanceTotal{}
			own[b.ItemID] = t
		}
		t.add(b)
	}
	stocks, err := s.recipeStockForItems(ctx, tenantID, recipeItems, nil)
	if err != nil {
		return nil, err
	}
	out := make([]StockAvailability, 0, len(itms))
	for _, itm := range itms {
		var rs *recipeStock
		if v, ok := stocks[itm.ID]; ok {
			rs = &v
		}
		out = append(out, availabilityFor(itm, rs, own[itm.ID]))
	}
	return out, nil
}

// getRecipeAvailability resolves one RECIPE item: a production BOM reads its own balance, a
// menu recipe the portions its ingredients and sub-recipes can make (the same maths as the
// deduction path, so the figure agrees with what a sale removes), and a menu recipe with no
// lines or no recipe tracks no stock.
func (s *Service) getRecipeAvailability(ctx context.Context, tenantID uuid.UUID, itm *ent.Item) (*StockAvailability, error) {
	stocks, err := s.recipeStockForItems(ctx, tenantID, []*ent.Item{itm}, nil)
	if err != nil {
		return nil, err
	}
	var rs *recipeStock
	if v, ok := stocks[itm.ID]; ok {
		rs = &v
	}
	if rs != nil && rs.production {
		return s.getDirectAvailability(ctx, tenantID, itm)
	}
	avail := availabilityFor(itm, rs, nil)
	return &avail, nil
}

// recipePortionsForItems returns, per RECIPE item id whose active menu recipe has lines and is
// constrained by stock, how many whole portions its ingredients can make over the warehouse
// scope (nil means every warehouse). Items with no BOM, an unconstrained BOM or a production
// BOM are absent; productionItems lists the production BOM items, which show their own balance.
func (s *Service) recipePortionsForItems(ctx context.Context, tenantID uuid.UUID, recipeItems []*ent.Item, scope map[uuid.UUID]struct{}) (portions map[uuid.UUID]float64, productionItems map[uuid.UUID]struct{}) {
	portions = make(map[uuid.UUID]float64)
	productionItems = make(map[uuid.UUID]struct{})
	stocks, err := s.recipeStockForItems(ctx, tenantID, recipeItems, scope)
	if err != nil {
		s.log.Warn("items: recipe portions for list", zap.Error(err))
	}
	for id, rs := range stocks {
		switch {
		case rs.production:
			productionItems[id] = struct{}{}
		case rs.hasBOM && rs.portions != stockcalc.Unlimited:
			portions[id] = rs.portions
		}
	}
	return portions, productionItems
}

// BOMAvailabilityResult represents the BOM-aware availability for a single SKU.
type BOMAvailabilityResult struct {
	SKU       string    `json:"sku"`
	ItemID    uuid.UUID `json:"item_id"`
	Available float64   `json:"available"`
	// TracksStock is false for an item that never runs out (a service, a voucher, a menu
	// recipe with no BOM or none constraining stock): Available is then 0 and means nothing.
	TracksStock   bool   `json:"tracks_stock"`
	Type          string `json:"type"` // "recipe" or "simple"
	UnitOfMeasure string `json:"unit_of_measure,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

// activeItemsBySKU loads the tenant's active items for the SKUs in one query, in the order the
// SKUs were asked for. Unknown SKUs are logged and skipped.
func (s *Service) activeItemsBySKU(ctx context.Context, tenantID uuid.UUID, skus []string, op string) ([]*ent.Item, error) {
	found, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.SkuIn(skus...), item.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query items: %w", err)
	}
	bySKU := make(map[string]*ent.Item, len(found))
	for _, it := range found {
		bySKU[it.Sku] = it
	}
	out := make([]*ent.Item, 0, len(found))
	seen := make(map[string]struct{}, len(skus))
	for _, sku := range skus {
		if _, dup := seen[sku]; dup {
			continue
		}
		seen[sku] = struct{}{}
		itm := bySKU[sku]
		if itm == nil {
			s.log.Warn(op+": item not found", zap.String("sku", sku))
			continue
		}
		out = append(out, itm)
	}
	return out, nil
}

// GetBOMAvailability returns BOM-aware availability for multiple SKUs: RECIPE items the portions
// their ingredients can make (production BOM items their own balance), other items their own
// balance, and tracks_stock=false for items that never run out. Fixed number of queries.
func (s *Service) GetBOMAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]BOMAvailabilityResult, error) {
	itms, err := s.activeItemsBySKU(ctx, tenantID, skus, "bom availability")
	if err != nil {
		return nil, err
	}
	avails, err := s.availabilityForItems(ctx, tenantID, itms)
	if err != nil {
		return nil, err
	}
	results := make([]BOMAvailabilityResult, 0, len(avails))
	for i, a := range avails {
		kind := "simple"
		if itms[i].Type == item.TypeRECIPE {
			kind = "recipe"
		}
		results = append(results, BOMAvailabilityResult{
			SKU:           a.SKU,
			ItemID:        a.ItemID,
			Available:     a.Available,
			TracksStock:   a.TracksStock,
			Type:          kind,
			UnitOfMeasure: a.UnitOfMeasure,
			UpdatedAt:     a.UpdatedAt,
		})
	}
	return results, nil
}

// BulkAvailability returns stock availability for multiple items by SKU, with the same rules as
// GetBOMAvailability and a fixed number of queries however many SKUs are asked for.
func (s *Service) BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]StockAvailability, error) {
	itms, err := s.activeItemsBySKU(ctx, tenantID, skus, "bulk availability")
	if err != nil {
		return nil, err
	}
	return s.availabilityForItems(ctx, tenantID, itms)
}
