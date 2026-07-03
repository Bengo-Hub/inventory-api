package recipes

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
)

// MenuEngineeringService classifies menu items using the Stars/Plowhorses/Puzzles/Dogs matrix.
type MenuEngineeringService struct {
	varianceSvc *VarianceService
	client      *ent.Client
	log         *zap.Logger
}

// NewMenuEngineeringService creates a new MenuEngineeringService.
func NewMenuEngineeringService(client *ent.Client, log *zap.Logger, orderingURL, posURL, apiKey string) *MenuEngineeringService {
	return &MenuEngineeringService{
		varianceSvc: NewVarianceService(client, log, orderingURL, posURL, apiKey),
		client:      client,
		log:         log.Named("recipes.menu_engineering"),
	}
}

type MenuCategory string

const (
	MenuCategoryStar      MenuCategory = "STAR"       // High popularity, high profit
	MenuCategoryPlowhorse MenuCategory = "PLOWHORSE"  // High popularity, low profit
	MenuCategoryPuzzle    MenuCategory = "PUZZLE"     // Low popularity, high profit
	MenuCategoryDog       MenuCategory = "DOG"        // Low popularity, low profit
)

// MenuMatrixItemDTO represents one recipe's classification in the matrix.
type MenuMatrixItemDTO struct {
	RecipeSKU      string       `json:"recipe_sku"`
	RecipeName     string       `json:"recipe_name"`
	Popularity     float64      `json:"popularity"`
	ContribMargin  float64      `json:"contrib_margin"`
	Category       MenuCategory `json:"category"`
	SuggestedPrice *float64     `json:"suggested_price,omitempty"`
}

// GetMatrix returns the menu engineering matrix for a period.
// It fetches sold quantities from the ordering service and computes profit margins.
func (s *MenuEngineeringService) GetMatrix(ctx context.Context, tenantID uuid.UUID, tenantSlug string, start, end time.Time) ([]MenuMatrixItemDTO, error) {
	// Load active recipes with cost data.
	recipes, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("menu_engineering: load recipes: %w", err)
	}
	if len(recipes) == 0 {
		return nil, nil
	}

	// Fetch units sold from ordering + POS sales (S2S).
	soldQty, err := s.varianceSvc.fetchSoldQty(ctx, tenantID, tenantSlug, start, end)
	if err != nil {
		s.log.Warn("menu_engineering: sales S2S failed, using zero sales", zap.Error(err))
		soldQty = map[string]float64{}
	}

	type entry struct {
		r          *ent.Recipe
		popularity float64
		margin     float64
	}

	entries := make([]entry, 0, len(recipes))
	for _, r := range recipes {
		qty := soldQty[r.Sku]
		var margin float64
		if r.SuggestedPrice != nil && r.CostPerPortion != nil && *r.SuggestedPrice > 0 {
			margin = (*r.SuggestedPrice - *r.CostPerPortion) / *r.SuggestedPrice * 100
		}
		entries = append(entries, entry{r: r, popularity: qty, margin: margin})
	}

	// Compute averages.
	var totalPop, totalMargin float64
	for _, e := range entries {
		totalPop += e.popularity
		totalMargin += e.margin
	}
	n := float64(len(entries))
	avgPop := totalPop / n
	avgMargin := totalMargin / n

	result := make([]MenuMatrixItemDTO, len(entries))
	for i, e := range entries {
		var cat MenuCategory
		switch {
		case e.popularity >= avgPop && e.margin >= avgMargin:
			cat = MenuCategoryStar
		case e.popularity >= avgPop && e.margin < avgMargin:
			cat = MenuCategoryPlowhorse
		case e.popularity < avgPop && e.margin >= avgMargin:
			cat = MenuCategoryPuzzle
		default:
			cat = MenuCategoryDog
		}
		result[i] = MenuMatrixItemDTO{
			RecipeSKU:      e.r.Sku,
			RecipeName:     e.r.Name,
			Popularity:     e.popularity,
			ContribMargin:  e.margin,
			Category:       cat,
			SuggestedPrice: e.r.SuggestedPrice,
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Popularity > result[j].Popularity
	})

	return result, nil
}
