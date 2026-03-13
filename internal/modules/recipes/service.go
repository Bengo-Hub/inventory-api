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
	ID            uuid.UUID              `json:"id"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	SKU           string                 `json:"sku"`
	Name          string                 `json:"name"`
	OutputQty     float64                `json:"output_qty"`
	UnitOfMeasure string                 `json:"unit_of_measure"`
	IsActive      bool                   `json:"is_active"`
	Ingredients   []RecipeIngredientDTO `json:"ingredients"`
}

// RecipeIngredientDTO represents a single ingredient in a recipe.
type RecipeIngredientDTO struct {
	ID            uuid.UUID `json:"id"`
	ItemID        uuid.UUID `json:"item_id"`
	ItemSKU       string    `json:"item_sku"`
	Quantity      float64   `json:"quantity"`
	UnitOfMeasure string    `json:"unit_of_measure"`
	Notes         string    `json:"notes"`
	DisplayOrder  int       `json:"display_order"`
}

// ListRecipes returns all recipes for a tenant.
func (s *Service) ListRecipes(ctx context.Context, tenantID uuid.UUID) ([]RecipeDTO, error) {
	recipes, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("recipes: list: %w", err)
	}

	result := make([]RecipeDTO, len(recipes))
	for i, r := range recipes {
		result[i] = s.toDTO(r)
	}
	return result, nil
}

// GetRecipe returns a single recipe by ID.
func (s *Service) GetRecipe(ctx context.Context, tenantID, id uuid.UUID) (*RecipeDTO, error) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.ID(id)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
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
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
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

	r, err := tx.Recipe.Create().
		SetTenantID(tenantID).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetOutputQty(dto.OutputQty).
		SetUnitOfMeasure(dto.UnitOfMeasure).
		SetIsActive(dto.IsActive).
		Save(ctx)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("recipes: create recipe: %w", err)
	}

	for i, ing := range dto.Ingredients {
		_, err := tx.RecipeIngredient.Create().
			SetRecipe(r).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNotes(ing.Notes).
			SetDisplayOrder(i).
			Save(ctx)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("recipes: create ingredient %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetRecipe(ctx, tenantID, r.ID)
}

// UpdateRecipe updates an existing recipe and its ingredients.
func (s *Service) UpdateRecipe(ctx context.Context, tenantID, id uuid.UUID, dto RecipeDTO) (*RecipeDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	_, err = tx.Recipe.Update().
		Where(recipe.TenantID(tenantID), recipe.ID(id)).
		SetSku(dto.SKU).
		SetName(dto.Name).
		SetOutputQty(dto.OutputQty).
		SetUnitOfMeasure(dto.UnitOfMeasure).
		SetIsActive(dto.IsActive).
		Save(ctx)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("recipes: update recipe: %w", err)
	}

	// Delete existing ingredients and re-create (simplest way to handle updates/removals)
	_, err = tx.RecipeIngredient.Delete().
		Where(recipeingredient.HasRecipeWith(recipe.ID(id))).
		Exec(ctx)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("recipes: clear old ingredients: %w", err)
	}

	for i, ing := range dto.Ingredients {
		_, err := tx.RecipeIngredient.Create().
			SetRecipeID(id).
			SetItemID(ing.ItemID).
			SetItemSku(ing.ItemSKU).
			SetQuantity(ing.Quantity).
			SetUnitOfMeasure(ing.UnitOfMeasure).
			SetNotes(ing.Notes).
			SetDisplayOrder(i).
			Save(ctx)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("recipes: update ingredient %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
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
		OutputQty:     r.OutputQty,
		UnitOfMeasure: r.UnitOfMeasure,
		IsActive:      r.IsActive,
		Ingredients:   make([]RecipeIngredientDTO, len(r.Edges.Ingredients)),
	}

	for i, ing := range r.Edges.Ingredients {
		dto.Ingredients[i] = RecipeIngredientDTO{
			ID:            ing.ID,
			ItemID:        ing.ItemID,
			ItemSKU:       ing.ItemSku,
			Quantity:      ing.Quantity,
			UnitOfMeasure: ing.UnitOfMeasure,
			Notes:         ing.Notes,
			DisplayOrder:  ing.DisplayOrder,
		}
	}
	return dto
}
