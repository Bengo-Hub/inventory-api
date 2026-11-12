package stock

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// A returned production item with no recorded consumption on its order goes back into its own
// balance on the direct path, with the same stock.updated event a returned goods item writes.
// A returned menu recipe still restocks nothing there: it never held stock of its own.
func TestDirectReturnRestock_ProductionItemVersusMenuRecipe(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_return_production_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop())

	tenantID := uuid.New()
	require.NoError(t, client.Tenant.Create().SetID(tenantID).SetName("Bakery").SetSlug("bakery-"+tenantID.String()[:8]).Exec(ctx))
	wh := client.Warehouse.Create().SetTenantID(tenantID).SetName("Main").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)

	newItem := func(sku string, typ item.Type, qty float64) *ent.Item {
		itm := client.Item.Create().SetTenantID(tenantID).SetSku(sku).SetName("Item " + sku).SetType(typ).SetCostPrice(10).SaveX(ctx)
		client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itm.ID).SetWarehouseID(wh.ID).
			SetOnHand(qty).SetAvailable(qty).ExecX(ctx)
		return itm
	}
	newRecipe := func(itm *ent.Item, kind recipe.Kind, lines map[*ent.Item]float64) {
		r := client.Recipe.Create().SetTenantID(tenantID).SetSku(itm.Sku).SetName("Recipe " + itm.Sku).
			SetItemID(itm.ID).SetKind(kind).SaveX(ctx)
		for ing, qty := range lines {
			client.RecipeIngredient.Create().SetRecipeID(r.ID).SetItemID(ing.ID).SetItemSku(ing.Sku).
				SetQuantity(qty).SetUnitOfMeasure("").ExecX(ctx)
		}
	}
	onHand := func(itm *ent.Item) float64 {
		return client.InventoryBalance.Query().
			Where(inventorybalance.ItemID(itm.ID), inventorybalance.WarehouseID(wh.ID)).OnlyX(ctx).OnHand
	}
	stockEvents := func(itm *ent.Item) int {
		return client.OutboxEvent.Query().
			Where(outboxevent.TenantID(tenantID), outboxevent.EventType("stock.updated"), outboxevent.AggregateID(itm.ID.String())).
			CountX(ctx)
	}
	restock := func(sku string, qty float64) *ReturnRestockResult {
		res, err := svc.RestockReturn(ctx, tenantID, ReturnRestockRequest{
			ReturnID: uuid.New(), ReturnNumber: "RET-" + sku, OrderID: uuid.New(), OrderNumber: "ORD-" + sku,
			WarehouseID: wh.ID, Lines: []ReturnRestockLine{{SKU: sku, Quantity: qty}},
			IdempotencyKey: "return-" + sku + "-" + uuid.NewString(), AllowDirectRestock: true,
		})
		require.NoError(t, err)
		return res
	}

	flour := newItem("FLOUR", item.TypeINGREDIENT, 100)
	soda := newItem("SODA", item.TypeGOODS, 10)

	loaf := newItem("LOAF", item.TypeRECIPE, 20)
	newRecipe(loaf, recipe.KindBom, map[*ent.Item]float64{flour: 5})

	pizza := newItem("PIZZA", item.TypeRECIPE, 0)
	newRecipe(pizza, recipe.KindMenu, map[*ent.Item]float64{flour: 2})

	t.Run("goods item restocks its own balance", func(t *testing.T) {
		res := restock("SODA", 2)
		require.Equal(t, ReturnRestocked, res.Status)
		require.Equal(t, 12.0, onHand(soda))
		require.Equal(t, 1, stockEvents(soda))
	})

	t.Run("production item restocks its own balance like goods", func(t *testing.T) {
		res := restock("LOAF", 3)
		require.Equal(t, ReturnRestocked, res.Status)
		require.Empty(t, res.Skipped)
		require.Len(t, res.Lines, 1)
		require.Equal(t, "LOAF", res.Lines[0].SKU)
		require.Equal(t, 3.0, res.Lines[0].Quantity)
		require.Equal(t, wh.ID, res.Lines[0].WarehouseID)
		require.Equal(t, "direct", res.Lines[0].Method)
		require.Equal(t, 23.0, onHand(loaf))
		require.Equal(t, 100.0, onHand(flour), "ingredients untouched")
		require.Equal(t, 1, stockEvents(loaf), "same stock.updated event the goods path writes")
	})

	t.Run("menu recipe restocks nothing on the direct path", func(t *testing.T) {
		res := restock("PIZZA", 1)
		require.Equal(t, ReturnNothingToRestock, res.Status)
		require.Equal(t, []string{"PIZZA"}, res.Skipped)
		require.Empty(t, res.Lines)
		require.Equal(t, 0.0, onHand(pizza))
		require.Equal(t, 100.0, onHand(flour))
		require.Zero(t, stockEvents(pizza))
	})
}
