package recipes

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/stocklevelevent"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// Recipe Health issue types. Each one is something that silently breaks stock or cost figures
// until a person fixes the data, so the UI lists them with direct fix actions.
const (
	// IssueMissingBOM: an active RECIPE item with no active recipe or no ingredient lines. Its
	// sales consume the item's own balance instead of ingredients (stock never reflects use).
	IssueMissingBOM = "missing_bom"
	// IssueUnconvertibleLine: a recipe line whose unit can't reach the ingredient's stock unit
	// and has no content-per-unit bridge, so the line never deducts stock.
	IssueUnconvertibleLine = "unconvertible_line"
	// IssueCostBasis: a measure-stocked ingredient whose cost per stock unit is more than 5x off
	// its own purchase basis (a per-gram price on a kg item and the like), which distorts every
	// recipe cost and COGS figure that uses it.
	IssueCostBasis = "cost_basis"
	// IssueFrequentStockOuts: items that hit zero three or more times in the last 30 days, the
	// usual sign of receipts not being recorded or a unit mismatch.
	IssueFrequentStockOuts = "frequent_stock_outs"
)

// HealthIssueTypes lists the issue types in display order.
var HealthIssueTypes = []string{IssueMissingBOM, IssueUnconvertibleLine, IssueCostBasis, IssueFrequentStockOuts}

// HealthRow is one flagged item. Fix targets are ids the UI deep-links to.
type HealthRow struct {
	Issue      string     `json:"issue"`
	ItemID     uuid.UUID  `json:"item_id"`
	ItemSKU    string     `json:"item_sku"`
	ItemName   string     `json:"item_name"`
	ItemType   string     `json:"item_type"`
	RecipeID   *uuid.UUID `json:"recipe_id,omitempty"`
	RecipeSKU  string     `json:"recipe_sku,omitempty"`
	RecipeName string     `json:"recipe_name,omitempty"`
	Detail     string     `json:"detail"`
	// NonDepleting reports the item's current mode so the UI can hide "Mark non-depleting"
	// once applied (a missing BOM stays flagged until ingredients are added).
	NonDepleting bool `json:"non_depleting"`
}

const stockOutWindow = 30 * 24 * time.Hour

// costBasisFactor mirrors units.PurchaseMismatchFactor: beyond 5x either way it is a unit mix-up,
// not a price movement.
const costBasisFactor = units.PurchaseMismatchFactor

// RecipeHealthSummary returns the number of flagged rows per issue type.
func (s *Service) RecipeHealthSummary(ctx context.Context, tenantID uuid.UUID) (map[string]int, error) {
	out := make(map[string]int, len(HealthIssueTypes))
	for _, issue := range HealthIssueTypes {
		rows, err := s.healthRows(ctx, tenantID, issue)
		if err != nil {
			return nil, err
		}
		out[issue] = len(rows)
	}
	return out, nil
}

// RecipeHealth returns one page of flagged rows for an issue type, plus the total.
func (s *Service) RecipeHealth(ctx context.Context, tenantID uuid.UUID, issue string, limit, offset int) ([]HealthRow, int, error) {
	rows, err := s.healthRows(ctx, tenantID, issue)
	if err != nil {
		return nil, 0, err
	}
	total := len(rows)
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 || offset >= total {
		return []HealthRow{}, total, nil
	}
	return rows[offset:min(offset+limit, total)], total, nil
}

func (s *Service) healthRows(ctx context.Context, tenantID uuid.UUID, issue string) ([]HealthRow, error) {
	var (
		rows []HealthRow
		err  error
	)
	switch issue {
	case IssueMissingBOM:
		rows, err = s.missingBOMRows(ctx, tenantID)
	case IssueUnconvertibleLine:
		rows, err = s.unconvertibleRows(ctx, tenantID)
	case IssueCostBasis:
		rows, err = s.costBasisRows(ctx, tenantID)
	case IssueFrequentStockOuts:
		rows, err = s.stockOutRows(ctx, tenantID)
	default:
		return nil, fmt.Errorf("recipes: unknown health issue %q", issue)
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ItemName < rows[j].ItemName })
	return rows, nil
}

func (s *Service) missingBOMRows(ctx context.Context, tenantID uuid.UUID) ([]HealthRow, error) {
	itms, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.IsActive(true),
			item.TypeEQ(item.TypeRECIPE),
			item.Not(item.HasProducedByRecipeWith(recipe.IsActive(true), recipe.HasIngredients())),
		).
		Select(item.FieldID, item.FieldSku, item.FieldName, item.FieldType, item.FieldStockTrackingMode).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: health missing bom: %w", err)
	}
	rows := make([]HealthRow, 0, len(itms))
	for _, it := range itms {
		rows = append(rows, HealthRow{
			Issue: IssueMissingBOM, ItemID: it.ID, ItemSKU: it.Sku, ItemName: it.Name, ItemType: string(it.Type),
			Detail:       "No ingredients configured: sales are not deducting any ingredient stock.",
			NonDepleting: it.StockTrackingMode == item.StockTrackingModeNonDepleting,
		})
	}
	return rows, nil
}

func (s *Service) unconvertibleRows(ctx context.Context, tenantID uuid.UUID) ([]HealthRow, error) {
	issues, err := s.AuditRecipeUnits(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	// Resolve the recipe's own item (the menu item) so a row can deep-link to it.
	recipeIDs := make([]uuid.UUID, 0, len(issues))
	for _, iss := range issues {
		recipeIDs = append(recipeIDs, iss.RecipeID)
	}
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IDIn(recipeIDs...)).
		WithItem(func(q *ent.ItemQuery) { q.Select(item.FieldID, item.FieldSku, item.FieldName, item.FieldType) }).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: health recipes: %w", err)
	}
	byID := make(map[uuid.UUID]*ent.Recipe, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	rows := make([]HealthRow, 0, len(issues))
	for _, iss := range issues {
		r := byID[iss.RecipeID]
		if r == nil || r.Edges.Item == nil {
			continue
		}
		rid := iss.RecipeID
		rows = append(rows, HealthRow{
			Issue: IssueUnconvertibleLine, ItemID: r.Edges.Item.ID, ItemSKU: r.Edges.Item.Sku, ItemName: r.Edges.Item.Name,
			ItemType: string(r.Edges.Item.Type), RecipeID: &rid, RecipeSKU: iss.RecipeSKU, RecipeName: iss.RecipeName,
			Detail: fmt.Sprintf("%s: %g %s can't convert to its stock unit %s. %s",
				iss.IngredientName, iss.Quantity, iss.LineUOM, iss.StockUnit, iss.Guidance),
		})
	}
	return rows, nil
}

func (s *Service) costBasisRows(ctx context.Context, tenantID uuid.UUID) ([]HealthRow, error) {
	itms, err := s.client.Item.Query().
		Where(
			item.TenantID(tenantID),
			item.IsActive(true),
			item.CostPriceGT(0),
			item.PurchasePriceGT(0),
			item.PurchasePackSizeGT(0),
		).
		WithUnits().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: health cost basis: %w", err)
	}
	rows := make([]HealthRow, 0)
	for _, it := range itms {
		if it.Edges.Units == nil || !units.IsMeasure(it.Edges.Units.Abbreviation) {
			continue // count-stocked items: a gap there is purchase data, not a unit mix-up
		}
		ep, ok := units.PurchaseCostPerBaseUnit(it.PurchasePrice, it.PurchasePackSize, it.YieldPct)
		if !ok || ep <= 0 {
			continue
		}
		ratio := *it.CostPrice / ep
		if ratio <= costBasisFactor && ratio >= 1/costBasisFactor {
			continue
		}
		rows = append(rows, HealthRow{
			Issue: IssueCostBasis, ItemID: it.ID, ItemSKU: it.Sku, ItemName: it.Name, ItemType: string(it.Type),
			Detail: fmt.Sprintf("Cost %.4g per %s, but its purchase price works out to %.4g per %s. Check the unit and cost.",
				*it.CostPrice, it.Edges.Units.Abbreviation, ep, it.Edges.Units.Abbreviation),
		})
	}
	return rows, nil
}

func (s *Service) stockOutRows(ctx context.Context, tenantID uuid.UUID) ([]HealthRow, error) {
	var counts []struct {
		ItemID uuid.UUID `json:"item_id"`
		Count  int       `json:"count"`
	}
	err := s.client.StockLevelEvent.Query().
		Where(
			stocklevelevent.TenantID(tenantID),
			stocklevelevent.EventTypeEQ(stocklevelevent.EventTypeOut),
			stocklevelevent.OccurredAtGTE(time.Now().Add(-stockOutWindow)),
		).
		GroupBy(stocklevelevent.FieldItemID).
		Aggregate(ent.Count()).
		Scan(ctx, &counts)
	if err != nil {
		return nil, fmt.Errorf("recipes: health stock-outs: %w", err)
	}
	ids := make([]uuid.UUID, 0)
	n := make(map[uuid.UUID]int)
	for _, c := range counts {
		if c.Count >= 3 {
			ids = append(ids, c.ItemID)
			n[c.ItemID] = c.Count
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	itms, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.IDIn(ids...)).
		Select(item.FieldID, item.FieldSku, item.FieldName, item.FieldType, item.FieldStockTrackingMode).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: health stock-out items: %w", err)
	}
	rows := make([]HealthRow, 0, len(itms))
	for _, it := range itms {
		rows = append(rows, HealthRow{
			Issue: IssueFrequentStockOuts, ItemID: it.ID, ItemSKU: it.Sku, ItemName: it.Name, ItemType: string(it.Type),
			Detail: fmt.Sprintf("Ran out %d times in the last 30 days. Check that deliveries are received in the system and that its unit matches how it is counted.", n[it.ID]),
			NonDepleting: it.StockTrackingMode == item.StockTrackingModeNonDepleting,
		})
	}
	return rows, nil
}

// RecomputeAllCosts recalculates every active recipe of the tenant (prep/BOM recipes first so menu
// recipes pick up fresh sub-recipe costs) through RecalculateRecipeCosts, which also writes each
// cost through to the recipe's item and publishes it, so POS and treasury COGS use the new figures.
// Returns how many recipes were recomputed.
func (s *Service) RecomputeAllCosts(ctx context.Context, tenantID uuid.UUID) (int, error) {
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true)).
		Select(recipe.FieldID, recipe.FieldKind).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("recipes: list for recompute: %w", err)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Kind != "menu" && recs[j].Kind == "menu" })
	done := 0
	for _, r := range recs {
		if rerr := s.RecalculateRecipeCosts(ctx, tenantID, r.ID); rerr != nil {
			s.log.Warn("recompute all: recipe failed", zap.String("recipe_id", r.ID.String()), zap.Error(rerr))
			continue
		}
		done++
	}
	return done, nil
}
