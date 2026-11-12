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

// A sold production item (made in advance by a production BOM) takes its own stock and leaves
// the ingredients alone; a made-to-order menu recipe still deducts its ingredients and reports
// each one as a consumption stock movement. Both paths are checked for direct sales and for
// reservations.
func TestSaleOfProductionItemVersusMenuRecipe(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_production_sale_")
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
		b := client.InventoryBalance.Query().
			Where(inventorybalance.ItemID(itm.ID), inventorybalance.WarehouseID(wh.ID)).OnlyX(ctx)
		return b.OnHand
	}
	stockEvents := func(itm *ent.Item) int {
		return client.OutboxEvent.Query().
			Where(outboxevent.TenantID(tenantID), outboxevent.EventType("stock.updated"), outboxevent.AggregateID(itm.ID.String())).
			CountX(ctx)
	}

	flour := newItem("FLOUR", item.TypeINGREDIENT, 100)
	yeast := newItem("YEAST", item.TypeINGREDIENT, 100)

	// A loaf baked in batches: the production run already used the flour and yeast and put 20
	// loaves on the shelf.
	loaf := newItem("LOAF", item.TypeRECIPE, 20)
	newRecipe(loaf, recipe.KindBom, map[*ent.Item]float64{flour: 5, yeast: 1})

	// A pizza made to order: no stock of its own, the sale deducts its ingredients.
	pizza := newItem("PIZZA", item.TypeRECIPE, 0)
	newRecipe(pizza, recipe.KindMenu, map[*ent.Item]float64{flour: 2, yeast: 0.5})

	t.Run("production item sale takes its own stock", func(t *testing.T) {
		_, err := svc.RecordConsumption(ctx, tenantID, ConsumptionRequest{
			OrderID: uuid.New(), WarehouseID: wh.ID, Items: []ConsumptionItem{{SKU: "LOAF", Quantity: 3}},
		})
		require.NoError(t, err)
		require.Equal(t, 17.0, onHand(loaf))
		require.Equal(t, 100.0, onHand(flour), "the run already used the flour")
		require.Equal(t, 100.0, onHand(yeast))
		require.Zero(t, stockEvents(flour), "no consumption event for ingredients")
		require.Zero(t, stockEvents(yeast))
	})

	t.Run("menu recipe sale deducts its ingredients", func(t *testing.T) {
		_, err := svc.RecordConsumption(ctx, tenantID, ConsumptionRequest{
			OrderID: uuid.New(), WarehouseID: wh.ID, Items: []ConsumptionItem{{SKU: "PIZZA", Quantity: 2}},
		})
		require.NoError(t, err)
		require.Equal(t, 96.0, onHand(flour))
		require.Equal(t, 99.0, onHand(yeast))
		require.Equal(t, 0.0, onHand(pizza), "a menu recipe holds no stock of its own")
		require.Equal(t, 1, stockEvents(flour), "each ingredient is reported as a consumption")
		require.Equal(t, 1, stockEvents(yeast))
	})

	t.Run("production item reservation holds and consumes its own stock", func(t *testing.T) {
		res, err := svc.CreateReservation(ctx, tenantID, ReservationRequest{
			OrderID: uuid.New(), WarehouseID: wh.ID, Items: []ReservationItem{{SKU: "LOAF", Quantity: 2}},
		})
		require.NoError(t, err)
		resv := client.Reservation.GetX(ctx, res.ID)
		require.Len(t, resv.Items, 1)
		require.Equal(t, "LOAF", resv.Items[0].SKU)
		require.Equal(t, 2.0, resv.Items[0].ReservedQty)
		require.False(t, resv.Items[0].Composite)

		_, err = svc.ConsumeReservation(ctx, tenantID, res.ID)
		require.NoError(t, err)
		require.Equal(t, 15.0, onHand(loaf))
		require.Equal(t, 96.0, onHand(flour), "ingredients untouched")
	})

	t.Run("menu recipe reservation consumes its ingredients", func(t *testing.T) {
		res, err := svc.CreateReservation(ctx, tenantID, ReservationRequest{
			OrderID: uuid.New(), WarehouseID: wh.ID, Items: []ReservationItem{{SKU: "PIZZA", Quantity: 1}},
		})
		require.NoError(t, err)
		_, err = svc.ConsumeReservation(ctx, tenantID, res.ID)
		require.NoError(t, err)
		require.Equal(t, 94.0, onHand(flour))
		require.Equal(t, 98.5, onHand(yeast))
		require.Equal(t, 0.0, onHand(pizza))
	})
}
