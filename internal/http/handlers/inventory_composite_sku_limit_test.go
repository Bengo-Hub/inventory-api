package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/bengobox/inventory-service/internal/http/handlers"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/units"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

type noUnits struct{}

func (noUnits) ListUnits(context.Context, uuid.UUID) ([]units.UnitDTO, error) { return nil, nil }
func (noUnits) CreateUnit(context.Context, uuid.UUID, units.UnitDTO) (*units.UnitDTO, error) {
	return nil, fmt.Errorf("not implemented")
}
func (noUnits) UpdateUnit(context.Context, uuid.UUID, uuid.UUID, units.UnitDTO) (*units.UnitDTO, error) {
	return nil, fmt.Errorf("not implemented")
}
func (noUnits) DeleteUnit(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// recipeRecorder implements only the two recipe calls the composite save makes.
type recipeRecorder struct {
	handlers.RecipesServicer
	created int
}

func (f *recipeRecorder) GetRecipeBySKU(context.Context, uuid.UUID, string) (*recipes.RecipeDTO, error) {
	return nil, fmt.Errorf("not found")
}

func (f *recipeRecorder) CreateRecipe(_ context.Context, _ uuid.UUID, dto recipes.RecipeDTO) (*recipes.RecipeDTO, error) {
	f.created++
	return &dto, nil
}

// compositeCatalog has `active` items, Sugar among them. Items CreateItem adds are found by later
// lookups, as in the real catalog.
func compositeCatalog(active int, created *[]string) *mockItemsSvc {
	catalog := []items.ItemDTO{{ID: uuid.New(), SKU: "ING-SUGAR", Name: "Sugar", Type: "INGREDIENT", IsActive: true}}
	return &mockItemsSvc{
		listItemsFn: func(_ context.Context, _ uuid.UUID, _, status string, _, _ int, _ *uuid.UUID, _ *uuid.UUID, search string, _ *uuid.UUID, _ *uuid.UUID, _ string, _ ...string) ([]items.ItemDTO, int, error) {
			if status == "active" && search == "" {
				return nil, active + len(*created), nil
			}
			var out []items.ItemDTO
			for _, it := range catalog {
				if strings.Contains(strings.ToLower(it.Name), strings.ToLower(search)) ||
					strings.Contains(strings.ToLower(it.SKU), strings.ToLower(search)) {
					out = append(out, it)
				}
			}
			return out, len(out), nil
		},
		getStockFn: func(_ context.Context, _ uuid.UUID, sku string) (*items.StockAvailability, error) {
			return nil, fmt.Errorf("items: item not found: sku=%s", sku) // the menu item is new
		},
		createItemFn: func(_ context.Context, _ uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error) {
			dto.ID = uuid.New()
			if dto.SKU == "" {
				dto.SKU = strings.ToUpper(dto.Name)
			}
			catalog = append(catalog, dto)
			*created = append(*created, dto.Name)
			return &dto, nil
		},
	}
}

func postComposite(t *testing.T, svc *mockItemsSvc, rec *recipeRecorder, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(body))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/%s/inventory/items/menu-item", testTenantID), &buf)
	claims := &authclient.Claims{TenantID: testTenantID.String(), SubscriptionLimits: map[string]int{subscriptions.LimitSKU: skuCap}}
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	h := handlers.NewInventoryHandler(zaptest.NewLogger(t), svc, &mockStockSvc{}, rec, noUnits{})
	w := httptest.NewRecorder()
	newChiRouter(h).ServeHTTP(w, req)
	return w
}

// The composite menu-item save counts the new menu item and every new ingredient as one batch
// against inventory_max_sku before writing anything. Reusing an existing ingredient takes no slot,
// and a new ingredient named on two lines counts once.
func TestCompositeMenuItemRespectsTheSKULimit(t *testing.T) {
	body := map[string]any{
		"sku": "LATTE", "name": "Latte", "selling_price": 250,
		"ingredients": []map[string]any{
			{"ingredient_name": "Milk", "qty": 0.2},
			{"ingredient_name": "milk", "qty": 0.05},
			{"ingredient_name": "Sugar", "qty": 0.01},
		},
	}

	// Two slots needed (Latte, Milk), one left: refused, nothing written.
	var created []string
	rec := &recipeRecorder{}
	w := postComposite(t, compositeCatalog(skuCap-1, &created), rec, body)
	require.Equal(t, http.StatusPaymentRequired, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "usage_limit_exceeded")
	require.Contains(t, w.Body.String(), `"metric":"skus"`)
	require.Empty(t, created)
	require.Equal(t, 0, rec.created)

	// Two slots left: the menu item and Milk are created once each, Sugar is reused.
	created, rec = nil, &recipeRecorder{}
	w = postComposite(t, compositeCatalog(skuCap-2, &created), rec, body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, []string{"Latte", "Milk"}, created)
	require.Equal(t, 1, rec.created)

	// Only Sugar, which already exists: one slot (the menu item) is enough.
	sugarOnly := map[string]any{
		"sku": "TEA", "name": "Sweet tea", "selling_price": 100,
		"ingredients": []map[string]any{{"ingredient_name": "Sugar", "qty": 0.01}},
	}
	created, rec = nil, &recipeRecorder{}
	w = postComposite(t, compositeCatalog(skuCap-1, &created), rec, sugarOnly)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, []string{"Sweet tea"}, created)
}
