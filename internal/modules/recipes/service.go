package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Service handles recipe (BOM) management.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new recipes service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("recipes.service"),
	}
}

// RecipeDTO represents a recipe with its ingredients.
type RecipeDTO struct {
	ID                  uuid.UUID             `json:"id"`
	TenantID            uuid.UUID             `json:"tenant_id"`
	SKU                 string                `json:"sku"`
	Name                string                `json:"name"`
	ItemName            string                `json:"item_name"`
	ItemID              *uuid.UUID            `json:"item_id,omitempty"`
	OutputQty           float64               `json:"output_qty"`
	Servings            float64               `json:"servings"`
	UnitOfMeasure       string                `json:"unit_of_measure"`
	IsActive            bool                  `json:"is_active"`
	Kind                string                `json:"kind"` // menu | bom
	RequiresQC          bool                  `json:"requires_qc"`
	TotalCost           *float64              `json:"total_cost"`
	CostPerPortion      *float64              `json:"cost_per_portion"`
	TargetMarginPercent *float64              `json:"target_margin_percent"`
	SuggestedPrice      *float64              `json:"suggested_price"`
	// Selling-price based costing (user-provided; never overwritten by system)
	SellingPrice    *float64 `json:"selling_price,omitempty"`
	FoodCostPct     *float64 `json:"food_cost_pct,omitempty"`
	Status          string   `json:"status,omitempty"` // OK - healthy | OK - above target FC% | LOSS - cost >= price
	PrepTimeMinutes *int     `json:"prep_time_minutes,omitempty"`
	Allergens       []string `json:"allergens"`
	Ingredients     []RecipeIngredientDTO `json:"ingredients"`
}

// RecipeIngredientDTO represents a single ingredient in a recipe.
type RecipeIngredientDTO struct {
	ID             uuid.UUID  `json:"id"`
	ItemID         uuid.UUID  `json:"item_id"`
	ItemSKU        string     `json:"item_sku"`
	ItemName       string     `json:"item_name"`
	ItemCostPrice  *float64   `json:"item_cost_price,omitempty"`
	// The ingredient item's own base/stock unit. ItemCostPrice is per this unit,
	// so a line written in another unit (e.g. ml against a per-L item) must be
	// converted before multiplying — clients need this to preview line costs.
	ItemUnitID     *uuid.UUID `json:"item_unit_id,omitempty"`
	// Content-per-unit bridge from the ingredient item (a 700 ml bottle stocked in
	// btl → 700 + "ml"). Clients need this to know a cross-dimension line (30 ml of
	// a btl-stocked bottle) deducts fractional stock units instead of flagging it
	// as un-deductible — mirrors stock.ConvertToStockUnit.
	ItemUnitContentQty *float64 `json:"item_unit_content_qty,omitempty"`
	ItemUnitContentUom string   `json:"item_unit_content_uom,omitempty"`
	Quantity       float64    `json:"quantity"`
	UnitOfMeasure  string     `json:"unit_of_measure"`
	UnitID         *uuid.UUID `json:"unit_id,omitempty"`
	WastePercent   float64    `json:"waste_percent"`
	Notes          string     `json:"notes"`
	DisplayOrder   int        `json:"display_order"`
	SubRecipeID    *uuid.UUID `json:"sub_recipe_id,omitempty"`
	SubRecipeName  string     `json:"sub_recipe_name,omitempty"`
}

// resolveItemSKUs fetches SKU strings for ingredient item IDs in one query.
func resolveItemSKUs(ctx context.Context, tx *ent.Tx, itemIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(itemIDs) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	its, err := tx.Item.Query().
		Where(entitem.IDIn(itemIDs...)).
		Select(entitem.FieldID, entitem.FieldSku).
		All(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[uuid.UUID]string, len(its))
	for _, it := range its {
		m[it.ID] = it.Sku
	}
	return m, nil
}

// ListRecipes returns a paginated list of recipes for a tenant.
// When outletID is set, only recipes whose linked item is accessible from that outlet are returned.
// Recipes with no item_id (standalone) are always included.
func (s *Service) ListRecipes(ctx context.Context, tenantID uuid.UUID, limit, offset int, outletID *uuid.UUID) ([]RecipeDTO, int, error) {
	q := s.client.Recipe.Query().Where(recipe.TenantID(tenantID))

	if outletID != nil {
		wIDs, _ := s.client.Warehouse.Query().
			Where(
				warehouse.TenantID(tenantID),
				warehouse.Or(
					warehouse.OutletIDEQ(*outletID),
					warehouse.OutletIDIsNil(),
				),
			).IDs(ctx)
		if len(wIDs) > 0 {
			bals, _ := s.client.InventoryBalance.Query().
				Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.WarehouseIDIn(wIDs...)).
				All(ctx)
			idSet := make(map[uuid.UUID]struct{}, len(bals))
			for _, b := range bals {
				idSet[b.ItemID] = struct{}{}
			}
			outletItemIDs := make([]uuid.UUID, 0, len(idSet))
			for id := range idSet {
				outletItemIDs = append(outletItemIDs, id)
			}
			q = q.Where(recipe.Or(recipe.ItemIDIsNil(), recipe.ItemIDIn(outletItemIDs...)))
		}
	}

	total, _ := q.Clone().Count(ctx)
	recs, err := q.
		WithIngredients(func(iq *ent.RecipeIngredientQuery) {
			iq.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem().
				WithUnit().
				WithSubRecipe()
		}).
		Order(ent.Asc(recipe.FieldName)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("recipes: list: %w", err)
	}

	nameMap := s.itemNamesForRecipes(ctx, recs)
	result := make([]RecipeDTO, len(recs))
	for i, r := range recs {
		var linkedName string
		if nameMap != nil && r.ItemID != nil {
			if info, ok := nameMap[*r.ItemID]; ok {
				linkedName = info[0]
			}
		}
		result[i] = s.toDTOWithItemName(r, linkedName)
	}
	return result, total, nil
}

// GetRecipe returns a single recipe by ID.
func (s *Service) GetRecipe(ctx context.Context, tenantID, id uuid.UUID) (*RecipeDTO, error) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ID(id)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem().
				WithUnit().
				WithSubRecipe()
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: get: %w", err)
	}
	nameMap := s.itemNamesForRecipes(ctx, []*ent.Recipe{r})
	var linkedName string
	if nameMap != nil && r.ItemID != nil {
		if info, ok := nameMap[*r.ItemID]; ok {
			linkedName = info[0]
		}
	}
	dto := s.toDTOWithItemName(r, linkedName)
	return &dto, nil
}

// GetRecipeBySKU returns a single recipe by SKU.
func (s *Service) GetRecipeBySKU(ctx context.Context, tenantID uuid.UUID, skuCode string) (*RecipeDTO, error) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(skuCode)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem().
				WithUnit().
				WithSubRecipe()
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: get by sku: %w", err)
	}
	nameMap := s.itemNamesForRecipes(ctx, []*ent.Recipe{r})
	var linkedName string
	if nameMap != nil && r.ItemID != nil {
		if info, ok := nameMap[*r.ItemID]; ok {
			linkedName = info[0]
		}
	}
	dto := s.toDTOWithItemName(r, linkedName)
	return &dto, nil
}

// CreateRecipe creates a new recipe.
func (s *Service) CreateRecipe(ctx context.Context, tenantID uuid.UUID, dto RecipeDTO) (*RecipeDTO, error) {
	// Reject lines whose units can never deduct stock (cross-dimension, no bridge) —
	// bad unit data becomes inexpressible instead of silently corrupting balances.
	if err := s.validateIngredientUnits(ctx, tenantID, dto.Ingredients); err != nil {
		return nil, err
	}
	// Lines referencing prepared items (produced by a prep recipe) ride that recipe
	// automatically for costing + backflush.
	s.autoLinkSubRecipes(ctx, tenantID, nil, dto.Ingredients)

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	create := tx.Recipe.Create().
		SetTenantID(tenantID).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetOutputQty(dto.OutputQty).
		SetUnitOfMeasure(dto.UnitOfMeasure).
		SetIsActive(dto.IsActive).
		SetKind(recipeKind(dto.Kind)).
		SetRequiresQc(dto.RequiresQC).
		SetNillableItemID(dto.ItemID).
		SetNillableTargetMarginPercent(dto.TargetMarginPercent).
		SetNillableSellingPrice(dto.SellingPrice)

	r, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("recipes: create recipe: %w", err)
	}

	// Resolve item SKUs for ingredients that omit them (frontend may not send item_sku).
	if len(dto.Ingredients) > 0 {
		ingIDs := make([]uuid.UUID, 0, len(dto.Ingredients))
		for _, ing := range dto.Ingredients {
			ingIDs = append(ingIDs, ing.ItemID)
		}
		skuMap, skuErr := resolveItemSKUs(ctx, tx, ingIDs)
		if skuErr == nil {
			for j := range dto.Ingredients {
				if dto.Ingredients[j].ItemSKU == "" {
					dto.Ingredients[j].ItemSKU = skuMap[dto.Ingredients[j].ItemID]
				}
			}
		}
	}

	for i, ing := range dto.Ingredients {
		b := tx.RecipeIngredient.Create().
			SetRecipe(r).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNillableUnitID(ing.UnitID).
			SetNillableSubRecipeID(ing.SubRecipeID).
			SetWastePercent(ing.WastePercent).
			SetNotes(ing.Notes).
			SetDisplayOrder(i)
		if _, err := b.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("recipes: create ingredient %d: %w", i, err)
		}
	}

	// Emit recipe.created event in the same transaction.
	itemIDStr := ""
	if dto.ItemID != nil {
		itemIDStr = dto.ItemID.String()
	}
	s.writeOutboxEvent(ctx, tx, tenantID, r.ID, "recipe", "recipe.changed", map[string]any{
		"recipe_id": r.ID.String(),
		"sku":       dto.SKU,
		"item_id":   itemIDStr,
		"action":    "created",
	})

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Recalculate costs after create (needs committed data).
	if calcErr := s.RecalculateRecipeCosts(ctx, tenantID, r.ID); calcErr != nil {
		s.log.Warn("recipe cost calculation failed after create", zap.Error(calcErr))
	}

	return s.GetRecipe(ctx, tenantID, r.ID)
}

// UpdateRecipe updates an existing recipe and its ingredients.
func (s *Service) UpdateRecipe(ctx context.Context, tenantID, id uuid.UUID, dto RecipeDTO) (*RecipeDTO, error) {
	// Same write-time unit guard + prepared-item auto-link as CreateRecipe.
	if err := s.validateIngredientUnits(ctx, tenantID, dto.Ingredients); err != nil {
		return nil, err
	}
	s.autoLinkSubRecipes(ctx, tenantID, &id, dto.Ingredients)

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	update := tx.Recipe.Update().
		Where(recipe.TenantID(tenantID), recipe.ID(id)).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetOutputQty(dto.OutputQty).
		SetUnitOfMeasure(dto.UnitOfMeasure).
		SetIsActive(dto.IsActive).
		SetKind(recipeKind(dto.Kind)).
		SetRequiresQc(dto.RequiresQC).
		SetNillableTargetMarginPercent(dto.TargetMarginPercent).
		SetNillableSellingPrice(dto.SellingPrice)

	if dto.ItemID != nil {
		update = update.SetNillableItemID(dto.ItemID)
	}

	if _, err = update.Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("recipes: update recipe: %w", err)
	}

	// Delete existing ingredients and re-create.
	if _, err = tx.RecipeIngredient.Delete().
		Where(recipeingredient.HasRecipeWith(recipe.ID(id))).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("recipes: clear old ingredients: %w", err)
	}

	// Resolve item SKUs for ingredients that omit them.
	if len(dto.Ingredients) > 0 {
		ingIDs := make([]uuid.UUID, 0, len(dto.Ingredients))
		for _, ing := range dto.Ingredients {
			ingIDs = append(ingIDs, ing.ItemID)
		}
		skuMap, skuErr := resolveItemSKUs(ctx, tx, ingIDs)
		if skuErr == nil {
			for j := range dto.Ingredients {
				if dto.Ingredients[j].ItemSKU == "" {
					dto.Ingredients[j].ItemSKU = skuMap[dto.Ingredients[j].ItemID]
				}
			}
		}
	}

	for i, ing := range dto.Ingredients {
		b := tx.RecipeIngredient.Create().
			SetRecipeID(id).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNillableUnitID(ing.UnitID).
			SetNillableSubRecipeID(ing.SubRecipeID).
			SetWastePercent(ing.WastePercent).
			SetNotes(ing.Notes).
			SetDisplayOrder(i)
		if _, err := b.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("recipes: update ingredient %d: %w", i, err)
		}
	}

	// Emit recipe.changed event before committing.
	updItemIDStr := ""
	if dto.ItemID != nil {
		updItemIDStr = dto.ItemID.String()
	}
	s.writeOutboxEvent(ctx, tx, tenantID, id, "recipe", "recipe.changed", map[string]any{
		"recipe_id": id.String(),
		"sku":       dto.SKU,
		"item_id":   updItemIDStr,
		"action":    "updated",
	})

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Recalculate costs after ingredients are saved (needs committed data for item cost_price lookup).
	if calcErr := s.RecalculateRecipeCosts(ctx, tenantID, id); calcErr != nil {
		s.log.Warn("recipe cost calculation failed after update", zap.Error(calcErr))
	}

	return s.GetRecipe(ctx, tenantID, id)
}

// DeleteRecipe removes a recipe.
func (s *Service) DeleteRecipe(ctx context.Context, tenantID, id uuid.UUID) error {
	_, err := s.client.Recipe.Delete().
		Where(recipe.TenantID(tenantID), recipe.ID(id)).
		Exec(ctx)
	return err
}

// itemNamesForRecipes batch-fetches item names/SKUs for recipes that have a non-nil item_id.
// Returns a map of item UUID → (name, sku).
func (s *Service) itemNamesForRecipes(ctx context.Context, recs []*ent.Recipe) map[uuid.UUID][2]string {
	ids := make([]uuid.UUID, 0, len(recs))
	for _, r := range recs {
		if r.ItemID != nil {
			ids = append(ids, *r.ItemID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	items, err := s.client.Item.Query().Where(entitem.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil
	}
	m := make(map[uuid.UUID][2]string, len(items))
	for _, it := range items {
		m[it.ID] = [2]string{it.Name, it.Sku}
	}
	return m
}

func (s *Service) toDTO(r *ent.Recipe) RecipeDTO {
	return s.toDTOWithItemName(r, "")
}

// recipeKind normalizes a free-form kind string to the recipe.Kind enum,
// defaulting to "menu" for anything unrecognized.
func recipeKind(s string) recipe.Kind {
	if s == string(recipe.KindBom) {
		return recipe.KindBom
	}
	return recipe.KindMenu
}

func (s *Service) toDTOWithItemName(r *ent.Recipe, linkedItemName string) RecipeDTO {
	itemName := linkedItemName
	if itemName == "" {
		itemName = r.Name
	}
	dto := RecipeDTO{
		ID:            r.ID,
		TenantID:      r.TenantID,
		SKU:           r.Sku,
		Name:          r.Name,
		ItemName:      itemName,
		ItemID:        r.ItemID,
		OutputQty:     r.OutputQty,
		Servings:      r.OutputQty,
		UnitOfMeasure: r.UnitOfMeasure,
		IsActive:      r.IsActive,
		Kind:          string(r.Kind),
		RequiresQC:    r.RequiresQc,
		TotalCost:           r.TotalCost,
		CostPerPortion:      r.CostPerPortion,
		TargetMarginPercent: r.TargetMarginPercent,
		SuggestedPrice:      r.SuggestedPrice,
		SellingPrice:        r.SellingPrice,
		FoodCostPct:         r.FoodCostPct,
		Status:              r.Status,
		PrepTimeMinutes:     r.PrepTimeMinutes,
		Allergens:     []string{},
		Ingredients:   make([]RecipeIngredientDTO, len(r.Edges.Ingredients)),
	}

	allergenSet := map[string]struct{}{}

	for i, ing := range r.Edges.Ingredients {
		ingDTO := RecipeIngredientDTO{
			ID:            ing.ID,
			ItemID:        ing.ItemID,
			ItemSKU:       ing.ItemSku,
			Quantity:      ing.Quantity,
			UnitOfMeasure: ing.UnitOfMeasure,
			WastePercent:  ing.WastePercent,
			Notes:         ing.Notes,
			DisplayOrder:  ing.DisplayOrder,
		}
		if ing.UnitID != nil {
			ingDTO.UnitID = ing.UnitID
		}
		if ing.Edges.Item != nil {
			ingDTO.ItemName = ing.Edges.Item.Name
			ingDTO.ItemCostPrice = ing.Edges.Item.CostPrice
			ingDTO.ItemUnitID = ing.Edges.Item.UnitID
			ingDTO.ItemUnitContentQty = ing.Edges.Item.UnitContentQty
			ingDTO.ItemUnitContentUom = ing.Edges.Item.UnitContentUom
			for _, tag := range ing.Edges.Item.Tags {
				if strings.HasPrefix(tag, "contains_") {
					allergenSet[tag] = struct{}{}
				}
			}
		}
		if ing.SubRecipeID != nil {
			ingDTO.SubRecipeID = ing.SubRecipeID
		}
		if ing.Edges.SubRecipe != nil {
			ingDTO.SubRecipeName = ing.Edges.SubRecipe.Name
		}
		dto.Ingredients[i] = ingDTO
	}

	for tag := range allergenSet {
		dto.Allergens = append(dto.Allergens, tag)
	}
	sort.Strings(dto.Allergens)

	return dto
}

// writeOutboxEvent stores a domain event in the outbox within an Ent transaction.
// Non-fatal — logs on failure so the business operation still succeeds.
func (s *Service) writeOutboxEvent(ctx context.Context, tx *ent.Tx, tenantID, aggregateID uuid.UUID, aggregateType, eventType string, payload map[string]any) {
	evt := events.NewEvent(eventType, aggregateType, aggregateID, tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		s.log.Warn("recipes outbox: marshal event", zap.Error(err), zap.String("event_type", eventType))
		return
	}
	_, err = tx.OutboxEvent.Create().
		SetTenantID(tenantID).
		SetAggregateType(aggregateType).
		SetAggregateID(aggregateID.String()).
		SetEventType(eventType).
		SetPayload(json.RawMessage(data)).
		Save(ctx)
	if err != nil {
		s.log.Warn("recipes outbox: write event", zap.Error(err), zap.String("event_type", eventType))
	}
}

// RecalculateCostsForIngredient recalculates costs for all recipes that use the given
// ingredient item ID. Called when an ingredient's cost_price changes.
func (s *Service) RecalculateCostsForIngredient(ctx context.Context, tenantID, ingredientItemID uuid.UUID) error {
	// Find all recipes that reference this ingredient.
	rIngredients, err := s.client.RecipeIngredient.Query().
		Where(recipeingredient.ItemID(ingredientItemID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("recipes: find affected recipes for ingredient %s: %w", ingredientItemID, err)
	}

	seen := make(map[uuid.UUID]struct{}, len(rIngredients))
	for _, ri := range rIngredients {
		recipeID := ri.RecipeID
		if _, ok := seen[recipeID]; ok {
			continue
		}
		seen[recipeID] = struct{}{}

		// Verify the recipe belongs to the same tenant.
		r, rErr := s.client.Recipe.Get(ctx, recipeID)
		if rErr != nil || r.TenantID != tenantID {
			continue
		}

		if calcErr := s.RecalculateRecipeCosts(ctx, tenantID, recipeID); calcErr != nil {
			s.log.Warn("recipes: cascade recalc failed",
				zap.String("recipe_id", recipeID.String()),
				zap.Error(calcErr),
			)
		}
	}
	return nil
}
