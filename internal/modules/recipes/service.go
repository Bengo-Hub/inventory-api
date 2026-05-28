package recipes

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
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
	ID                  uuid.UUID              `json:"id"`
	TenantID            uuid.UUID              `json:"tenant_id"`
	SKU                 string                 `json:"sku"`
	Name                string                 `json:"name"`
	ItemName            string                 `json:"item_name"`
	ItemID              *uuid.UUID             `json:"item_id,omitempty"`
	OutputQty           float64                `json:"output_qty"`
	Servings            float64                `json:"servings"`
	UnitOfMeasure       string                 `json:"unit_of_measure"`
	IsActive            bool                   `json:"is_active"`
	TotalCost           *float64               `json:"total_cost"`
	CostPerPortion      *float64               `json:"cost_per_portion"`
	TargetMarginPercent *float64               `json:"target_margin_percent"`
	SuggestedPrice      *float64               `json:"suggested_price"`
	PrepTimeMinutes     *int                   `json:"prep_time_minutes,omitempty"`
	Ingredients         []RecipeIngredientDTO  `json:"ingredients"`
}

// RecipeIngredientDTO represents a single ingredient in a recipe.
type RecipeIngredientDTO struct {
	ID            uuid.UUID  `json:"id"`
	ItemID        uuid.UUID  `json:"item_id"`
	ItemSKU       string     `json:"item_sku"`
	ItemName      string     `json:"item_name"`
	Quantity      float64    `json:"quantity"`
	UnitOfMeasure string     `json:"unit_of_measure"`
	UnitID        *uuid.UUID `json:"unit_id,omitempty"`
	WastePercent  float64    `json:"waste_percent"`
	Notes         string     `json:"notes"`
	DisplayOrder  int        `json:"display_order"`
}

// ListRecipes returns a paginated list of recipes for a tenant.
func (s *Service) ListRecipes(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]RecipeDTO, int, error) {
	q := s.client.Recipe.Query().Where(recipe.TenantID(tenantID))
	total, _ := q.Clone().Count(ctx)
	recs, err := q.
		WithIngredients(func(iq *ent.RecipeIngredientQuery) {
			iq.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem().
				WithUnit()
		}).
		Order(ent.Asc(recipe.FieldName)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("recipes: list: %w", err)
	}

	result := make([]RecipeDTO, len(recs))
	for i, r := range recs {
		result[i] = s.toDTO(r)
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
				WithUnit()
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: get: %w", err)
	}
	dto := s.toDTO(r)
	return &dto, nil
}

// GetRecipeBySKU returns a single recipe by SKU.
func (s *Service) GetRecipeBySKU(ctx context.Context, tenantID uuid.UUID, skuCode string) (*RecipeDTO, error) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(skuCode)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)).
				WithItem().
				WithUnit()
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: get by sku: %w", err)
	}
	dto := s.toDTO(r)
	return &dto, nil
}

// CreateRecipe creates a new recipe.
func (s *Service) CreateRecipe(ctx context.Context, tenantID uuid.UUID, dto RecipeDTO) (*RecipeDTO, error) {
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
		SetNillableItemID(dto.ItemID).
		SetNillableTargetMarginPercent(dto.TargetMarginPercent)

	r, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("recipes: create recipe: %w", err)
	}

	for i, ing := range dto.Ingredients {
		_, err := tx.RecipeIngredient.Create().
			SetRecipe(r).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNillableUnitID(ing.UnitID).
			SetWastePercent(ing.WastePercent).
			SetNotes(ing.Notes).
			SetDisplayOrder(i).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("recipes: create ingredient %d: %w", i, err)
		}
	}

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
		SetNillableTargetMarginPercent(dto.TargetMarginPercent)

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

	for i, ing := range dto.Ingredients {
		_, err := tx.RecipeIngredient.Create().
			SetRecipeID(id).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNillableUnitID(ing.UnitID).
			SetWastePercent(ing.WastePercent).
			SetNotes(ing.Notes).
			SetDisplayOrder(i).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("recipes: update ingredient %d: %w", i, err)
		}
	}

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

func (s *Service) toDTO(r *ent.Recipe) RecipeDTO {
	dto := RecipeDTO{
		ID:            r.ID,
		TenantID:      r.TenantID,
		SKU:           r.Sku,
		Name:          r.Name,
		ItemName:      r.Name,
		ItemID:        r.ItemID,
		OutputQty:     r.OutputQty,
		Servings:      r.OutputQty,
		UnitOfMeasure: r.UnitOfMeasure,
		IsActive:      r.IsActive,
		TotalCost:           r.TotalCost,
		CostPerPortion:      r.CostPerPortion,
		TargetMarginPercent: r.TargetMarginPercent,
		SuggestedPrice:      r.SuggestedPrice,
		PrepTimeMinutes:     r.PrepTimeMinutes,
		Ingredients:   make([]RecipeIngredientDTO, len(r.Edges.Ingredients)),
	}

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
		}
		dto.Ingredients[i] = ingDTO
	}
	return dto
}
