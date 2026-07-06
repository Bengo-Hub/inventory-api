package recipes

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// UnitIssue describes one recipe line whose unit cannot be reconciled with the
// ingredient's stock unit (cross-dimension, no unit_content bridge, no sub-recipe).
// Such a line can never deduct stock correctly, so writes are rejected with guidance
// and existing rows are surfaced by the unit audit.
type UnitIssue struct {
	RecipeID       uuid.UUID `json:"recipe_id,omitempty"`
	RecipeSKU      string    `json:"recipe_sku,omitempty"`
	RecipeName     string    `json:"recipe_name,omitempty"`
	IngredientSKU  string    `json:"ingredient_sku"`
	IngredientName string    `json:"ingredient_name"`
	Quantity       float64   `json:"quantity"`
	LineUOM        string    `json:"line_uom"`
	StockUnit      string    `json:"stock_unit"`
	Guidance       string    `json:"guidance"`
}

// UnitValidationError carries the offending lines of a recipe write so handlers can
// return a 422 with actionable per-line guidance instead of a blind 500.
type UnitValidationError struct {
	Issues []UnitIssue
}

func (e *UnitValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		parts = append(parts, fmt.Sprintf("%s: %.4g %s vs stock unit %q", is.IngredientSKU, is.Quantity, is.LineUOM, is.StockUnit))
	}
	return "recipe has ingredient lines whose units cannot deduct stock: " + strings.Join(parts, "; ")
}

func unitGuidance(itemName string) string {
	return fmt.Sprintf(
		"%q is stocked in a different unit dimension than this line. Either: (a) model it as a prepared ingredient (a prep recipe + production batch that stocks it in the line's unit, e.g. Lemon Juice in ml made from lemons), (b) declare the item's content-per-unit (e.g. 750 ml per bottle) so fractional units deduct, or (c) change the line's unit to match how it is stocked.",
		itemName,
	)
}

// validateIngredientUnits rejects recipe writes containing lines that could never
// deduct stock: a line converts when it is same-dimension with the item's stock unit,
// bridges through the item's unit_content declaration, or rides a sub-recipe (the
// prep/backflush paths handle it). Anything else used to silently deduct a raw number
// — now it is inexpressible.
func (s *Service) validateIngredientUnits(ctx context.Context, tenantID uuid.UUID, ings []RecipeIngredientDTO) error {
	if len(ings) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(ings))
	for _, ing := range ings {
		if ing.ItemID != uuid.Nil {
			ids = append(ids, ing.ItemID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	items, err := s.client.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(ids...)).
		WithUnits().
		All(ctx)
	if err != nil {
		return fmt.Errorf("recipes: load ingredient items for unit validation: %w", err)
	}
	byID := make(map[uuid.UUID]*ent.Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}

	var issues []UnitIssue
	for _, ing := range ings {
		// Sub-recipe lines reconcile through the prep item / backflush explosion.
		if ing.SubRecipeID != nil {
			continue
		}
		itm, ok := byID[ing.ItemID]
		if !ok {
			continue // missing item surfaces as its own error downstream
		}
		if _, ok := stock.ConvertToStockUnit(itm, ing.Quantity, ing.UnitOfMeasure); !ok {
			stockUnit := ""
			if itm.Edges.Units != nil {
				stockUnit = itm.Edges.Units.Abbreviation
			}
			issues = append(issues, UnitIssue{
				IngredientSKU:  itm.Sku,
				IngredientName: itm.Name,
				Quantity:       ing.Quantity,
				LineUOM:        ing.UnitOfMeasure,
				StockUnit:      stockUnit,
				Guidance:       unitGuidance(itm.Name),
			})
		}
	}
	if len(issues) > 0 {
		return &UnitValidationError{Issues: issues}
	}
	return nil
}

// autoLinkSubRecipes fills in sub_recipe_id for ingredient lines whose item is itself
// produced by a prep recipe (Recipe.item_id → the ingredient's item). This is what makes
// the prepared-ingredient model zero-config: a menu recipe referencing "Lemon Juice"
// automatically rides its prep recipe — the deduction path prefers stocked prepared
// items and backflushes the prep BOM when unstocked. Self-references are skipped
// (a recipe is never its own sub-recipe).
func (s *Service) autoLinkSubRecipes(ctx context.Context, tenantID uuid.UUID, selfRecipeID *uuid.UUID, ings []RecipeIngredientDTO) {
	ids := make([]uuid.UUID, 0, len(ings))
	for _, ing := range ings {
		if ing.SubRecipeID == nil && ing.ItemID != uuid.Nil {
			ids = append(ids, ing.ItemID)
		}
	}
	if len(ids) == 0 {
		return
	}
	prepRecipes, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ItemIDIn(ids...), recipe.IsActive(true)).
		All(ctx)
	if err != nil {
		return
	}
	byItem := make(map[uuid.UUID]uuid.UUID, len(prepRecipes))
	for _, pr := range prepRecipes {
		if pr.ItemID == nil {
			continue
		}
		if selfRecipeID != nil && pr.ID == *selfRecipeID {
			continue
		}
		byItem[*pr.ItemID] = pr.ID
	}
	for i := range ings {
		if ings[i].SubRecipeID == nil {
			if rid, ok := byItem[ings[i].ItemID]; ok {
				linked := rid
				ings[i].SubRecipeID = &linked
			}
		}
	}
}

// AuditRecipeUnits scans ALL of a tenant's recipes for existing ingredient lines whose
// units cannot deduct stock (legacy/bulk-imported data written before the write-time
// guard). Read-only: powers the remediation report and the UI recipes audit.
func (s *Service) AuditRecipeUnits(ctx context.Context, tenantID uuid.UUID) ([]UnitIssue, error) {
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.WithItem(func(iq *ent.ItemQuery) { iq.WithUnits() })
		}).
		Order(ent.Asc(recipe.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: unit audit: %w", err)
	}

	issues := []UnitIssue{}
	for _, r := range recs {
		for _, ing := range r.Edges.Ingredients {
			if ing.SubRecipeID != nil || ing.Edges.Item == nil {
				continue
			}
			if _, ok := stock.ConvertToStockUnit(ing.Edges.Item, ing.Quantity, ing.UnitOfMeasure); ok {
				continue
			}
			stockUnit := ""
			if ing.Edges.Item.Edges.Units != nil {
				stockUnit = ing.Edges.Item.Edges.Units.Abbreviation
			}
			issues = append(issues, UnitIssue{
				RecipeID:       r.ID,
				RecipeSKU:      r.Sku,
				RecipeName:     r.Name,
				IngredientSKU:  ing.ItemSku,
				IngredientName: ing.Edges.Item.Name,
				Quantity:       ing.Quantity,
				LineUOM:        ing.UnitOfMeasure,
				StockUnit:      stockUnit,
				Guidance:       unitGuidance(ing.Edges.Item.Name),
			})
		}
	}
	return issues, nil
}
