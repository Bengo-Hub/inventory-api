package items

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// Recipe availability against a real schema: a menu recipe reads its portions (through a
// sub-recipe too), a menu recipe with no BOM and a service track no stock, a production recipe
// reads its own balance, the summary counts only stock-bearing zero rows, and a 200-item bulk
// request resolves every item.
func TestRecipeAvailabilityAndSummary(t *testing.T) {
	client, _ := testutil.SchemaDB(t, "inv_recipe_avail_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID := uuid.New()
	require.NoError(t, client.Tenant.Create().SetID(tenantID).SetName("Cafe").SetSlug("cafe-"+tenantID.String()[:8]).Exec(ctx))
	wh := client.Warehouse.Create().SetTenantID(tenantID).SetName("Main").SetCode("MAIN").SaveX(ctx)

	newItem := func(sku string, typ item.Type) uuid.UUID {
		return client.Item.Create().SetTenantID(tenantID).SetSku(sku).SetName("Item " + sku).SetType(typ).SaveX(ctx).ID
	}
	balance := func(itemID uuid.UUID, qty float64) {
		client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemID).SetWarehouseID(wh.ID).
			SetOnHand(qty).SetAvailable(qty).ExecX(ctx)
	}
	newRecipe := func(sku string, itemID uuid.UUID, kind recipe.Kind) uuid.UUID {
		return client.Recipe.Create().SetTenantID(tenantID).SetSku(sku).SetName("Recipe " + sku).
			SetItemID(itemID).SetKind(kind).SaveX(ctx).ID
	}
	// Lines carry no unit, so quantities pass through in the item's own unit.
	addLine := func(recipeID, itemID uuid.UUID, sku string, qty float64, sub *uuid.UUID) {
		client.RecipeIngredient.Create().SetRecipeID(recipeID).SetItemID(itemID).SetItemSku(sku).
			SetQuantity(qty).SetUnitOfMeasure("").SetNillableSubRecipeID(sub).ExecX(ctx)
	}

	flour := newItem("FLOUR", item.TypeINGREDIENT)
	balance(flour, 10)

	bread := newItem("BREAD", item.TypeRECIPE) // 2 flour a portion: 5 portions
	breadRecipe := newRecipe("BREAD", bread, recipe.KindMenu)
	addLine(breadRecipe, flour, "FLOUR", 2, nil)

	toast := newItem("TOAST", item.TypeRECIPE) // 1 bread a portion, bread has no stock of its own
	toastRecipe := newRecipe("TOAST", toast, recipe.KindMenu)
	addLine(toastRecipe, bread, "BREAD", 1, &breadRecipe)

	newItem("WATER-CUP", item.TypeRECIPE) // no recipe at all
	tea := newItem("TEA", item.TypeRECIPE)         // no recipe, a stray zero row
	balance(tea, 0)
	delivery := newItem("DELIVERY", item.TypeSERVICE) // a stray zero row
	balance(delivery, 0)

	loaf := newItem("LOAF-BATCH", item.TypeRECIPE) // production BOM: holds its own stock
	newRecipe("LOAF-BATCH", loaf, recipe.KindBom)
	balance(loaf, 0)

	soda := newItem("SODA", item.TypeGOODS)
	balance(soda, 0)

	skus := []string{"BREAD", "TOAST", "WATER-CUP", "TEA", "DELIVERY", "LOAF-BATCH", "SODA"}
	for i := 0; i < 200; i++ {
		sku := fmt.Sprintf("G-%03d", i)
		balance(newItem(sku, item.TypeGOODS), 50)
		skus = append(skus, sku)
	}

	t.Run("bulk availability", func(t *testing.T) {
		got, err := svc.BulkAvailability(ctx, tenantID, skus)
		require.NoError(t, err)
		require.Len(t, got, len(skus))
		by := make(map[string]StockAvailability, len(got))
		for _, a := range got {
			by[a.SKU] = a
		}
		require.True(t, by["BREAD"].TracksStock)
		require.Equal(t, 5.0, by["BREAD"].Available)
		require.True(t, by["TOAST"].TracksStock, "toast rides the bread sub-recipe")
		require.Equal(t, 5.0, by["TOAST"].Available)
		require.False(t, by["WATER-CUP"].TracksStock)
		require.False(t, by["TEA"].TracksStock)
		require.False(t, by["DELIVERY"].TracksStock)
		require.True(t, by["LOAF-BATCH"].TracksStock)
		require.Equal(t, 0.0, by["LOAF-BATCH"].Available)
		require.True(t, by["SODA"].TracksStock)
		require.Equal(t, 50.0, by["G-199"].Available)
	})

	t.Run("single item and bom endpoint agree", func(t *testing.T) {
		one, err := svc.GetStockAvailability(ctx, tenantID, "WATER-CUP")
		require.NoError(t, err)
		require.False(t, one.TracksStock)
		bom, err := svc.GetBOMAvailability(ctx, tenantID, []string{"TOAST", "DELIVERY"})
		require.NoError(t, err)
		require.Len(t, bom, 2)
		require.Equal(t, 5.0, bom[0].Available)
		require.True(t, bom[0].TracksStock)
		require.False(t, bom[1].TracksStock)
	})

	t.Run("summary counts only stock-bearing zero rows", func(t *testing.T) {
		sum, err := svc.GetInventorySummary(ctx, tenantID)
		require.NoError(t, err)
		require.Equal(t, 2, sum.OutOfStockItems, "SODA and the production LOAF-BATCH, never TEA or DELIVERY")
		require.Equal(t, 1, sum.LowStockItems, "FLOUR at 10")
	})
}
