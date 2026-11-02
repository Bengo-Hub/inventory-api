package items

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// Deadstock and Fast-Moving follow the location they are scoped to: stock and movement both come
// from the scoped warehouses, so an item selling at one location is still dead stock where it sits
// idle, a backordered location holds nothing, stock-in reasons never count as movement, and an
// outlet with no warehouse of its own shows nothing rather than the whole tenant.
func TestMovementReportsFollowTheLocationScope(t *testing.T) {
	client, db := testutil.SchemaDB(t, "inv_movement_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)
	svc.SetReportDB(db)

	tenantID := uuid.New()
	require.NoError(t, client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-"+tenantID.String()[:8]).Exec(ctx))
	wh1 := client.Warehouse.Create().SetTenantID(tenantID).SetName("Eldoret").SetCode("ELD").SaveX(ctx)
	wh2 := client.Warehouse.Create().SetTenantID(tenantID).SetName("Busia").SetCode("BUS").SaveX(ctx)

	item := func(sku string, cost float64) uuid.UUID {
		return client.Item.Create().SetTenantID(tenantID).SetSku(sku).SetName("Item " + sku).SetCostPrice(cost).SaveX(ctx).ID
	}
	phone, charger, flour := item("PHONE", 10), item("CHARGER", 5), item("FLOUR", 2)
	balance := func(itemID, whID uuid.UUID, onHand float64) {
		client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(itemID).SetWarehouseID(whID).
			SetOnHand(onHand).SetAvailable(onHand).SetUpdatedAt(time.Now().AddDate(0, 0, -400)).ExecX(ctx)
	}
	balance(phone, wh1.ID, 10)
	balance(phone, wh2.ID, 4)
	balance(charger, wh1.ID, 3)
	balance(flour, wh1.ID, 6)
	balance(flour, wh2.ID, -2) // backordered at Busia

	used := func(whID uuid.UUID, reason, sku string, qty float64, daysAgo int) {
		at := time.Now().AddDate(0, 0, -daysAgo)
		client.Consumption.Create().SetTenantID(tenantID).SetOrderID(uuid.New()).SetWarehouseID(whID).
			SetItems([]schema.ConsumptionItemJSON{{SKU: sku, Quantity: qty}}).SetReason(reason).
			SetProcessedAt(at).SetCreatedAt(at).ExecX(ctx)
	}
	used(wh2.ID, "pos_sale", " phone ", 3, 5)        // phones sell in Busia, not Eldoret
	used(wh1.ID, "sale", "CHARGER", 1, 200)          // chargers last sold outside the window
	used(wh1.ID, "opening_balance", "CHARGER", 9, 1) // stock-in is not movement
	used(wh1.ID, "production", "FLOUR", 1, 10)       // flour is used up in production

	t.Run("deadstock across every location", func(t *testing.T) {
		rep, err := svc.StockDeadstock(ctx, tenantID, 90, nil)
		require.NoError(t, err)
		require.Equal(t, 1, rep.ItemCount)
		require.InDelta(t, 15, rep.TotalDeadValue, 0.001)
		require.Equal(t, charger, rep.Items[0].ItemID)
		require.WithinDuration(t, time.Now().AddDate(0, 0, -200), rep.Items[0].LastActivity, time.Minute)
	})
	t.Run("deadstock at one location", func(t *testing.T) {
		rep, err := svc.StockDeadstock(ctx, tenantID, 90, []uuid.UUID{wh1.ID})
		require.NoError(t, err)
		require.Equal(t, 2, rep.ItemCount)
		require.InDelta(t, 115, rep.TotalDeadValue, 0.001)
		require.Equal(t, []uuid.UUID{phone, charger}, []uuid.UUID{rep.Items[0].ItemID, rep.Items[1].ItemID})
		require.InDelta(t, 10, rep.Items[0].OnHand, 0.001)
	})
	t.Run("deadstock where the only idle line is backordered", func(t *testing.T) {
		rep, err := svc.StockDeadstock(ctx, tenantID, 90, []uuid.UUID{wh2.ID})
		require.NoError(t, err)
		require.Zero(t, rep.ItemCount)
		require.Empty(t, rep.Items)
	})
	t.Run("an outlet with no warehouse", func(t *testing.T) {
		rep, err := svc.StockDeadstock(ctx, tenantID, 90, []uuid.UUID{})
		require.NoError(t, err)
		require.Zero(t, rep.ItemCount)
		fast, err := svc.StockFastMoving(ctx, tenantID, 90, []uuid.UUID{})
		require.NoError(t, err)
		require.Zero(t, fast.ItemCount)
	})
	t.Run("fast-moving across every location", func(t *testing.T) {
		rep, err := svc.StockFastMoving(ctx, tenantID, 90, nil)
		require.NoError(t, err)
		require.Equal(t, 2, rep.ItemCount)
		require.Equal(t, phone, rep.Items[0].ItemID)
		require.InDelta(t, 3, rep.Items[0].QuantityMoved, 0.001)
		require.InDelta(t, 14, rep.Items[0].OnHand, 0.001)
		require.Equal(t, flour, rep.Items[1].ItemID)
	})
	t.Run("fast-moving at one location", func(t *testing.T) {
		rep, err := svc.StockFastMoving(ctx, tenantID, 90, []uuid.UUID{wh1.ID})
		require.NoError(t, err)
		require.Equal(t, 1, rep.ItemCount)
		require.Equal(t, flour, rep.Items[0].ItemID)
		require.InDelta(t, 6, rep.Items[0].OnHand, 0.001)
	})
}
