package stock

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	"github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	"github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// TestItemStockHistory_PurchaseReturnNotSold pins the boi-enterprises 2026-09-25 report: an
// approved purchase return showed in the item's stock history as "SOLD / Order 2586cf93".
//
//   - The new path (a "purchase_return" stock adjustment referencing the return number) must
//     read as Purchase Return with the return number and supplier, count toward
//     total_purchase_returns (not total_sold), and publish NO valued stock.adjusted event
//     (treasury values it through the vendor credit note).
//   - A legacy row (written through the sales consumption path with reason "purchase_return"
//     and the return id as order id) must read the same way.
//
// Requires a real local Postgres via POSTGRES_URL; skipped when unset.
func TestItemStockHistory_PurchaseReturnNotSold(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed stock history test")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() {
		_, _ = client.ConsumptionLine.Delete().Where(entconsumptionline.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Consumption.Delete().Where(entconsumption.TenantID(tenantID)).Exec(ctx)
		_, _ = client.StockAdjustment.Delete().Where(stockadjustment.TenantID(tenantID)).Exec(ctx)
		_, _ = client.InventoryBalance.Delete().Where(inventorybalance.TenantID(tenantID)).Exec(ctx)
		_, _ = client.PurchaseReturn.Delete().Where(purchasereturn.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Supplier.Delete().Where(supplier.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Item.Delete().Where(item.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Warehouse.Delete().Where(warehouse.TenantID(tenantID)).Exec(ctx)
		_, _ = client.OutboxEvent.Delete().Where(outboxevent.TenantID(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Purchase Return History Test").
		SetSlug("pr-history-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)

	wh, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Main Store").SetCode("MAIN").
		SetIsDefault(true).Save(ctx)
	require.NoError(t, err)
	cost := 100.0
	it, err := client.Item.Create().SetTenantID(tenantID).SetSku("PR-TEST-1").SetName("Rice 1kg").
		SetCostPrice(cost).Save(ctx)
	require.NoError(t, err)
	_, err = client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(it.ID).SetWarehouseID(wh.ID).
		SetOnHand(76).SetAvailable(76).Save(ctx)
	require.NoError(t, err)
	sup, err := client.Supplier.Create().SetTenantID(tenantID).SetName("ELECTROMATE").SetCode("ELEC").Save(ctx)
	require.NoError(t, err)

	newReturn, err := client.PurchaseReturn.Create().SetTenantID(tenantID).SetReturnNumber("PRET-TEST-NEW").
		SetSupplierID(sup.ID).SetWarehouseID(wh.ID).Save(ctx)
	require.NoError(t, err)
	legacyReturn, err := client.PurchaseReturn.Create().SetTenantID(tenantID).SetReturnNumber("PRET-TEST-OLD").
		SetSupplierID(sup.ID).Save(ctx)
	require.NoError(t, err)

	svc := NewService(client, zap.NewNop())

	// New path: approval takes the goods out as a purchase_return adjustment.
	_, err = svc.AdjustStock(ctx, tenantID, AdjustStockRequest{
		SKU: it.Sku, Adjustment: -1, Reason: string(stockadjustment.ReasonPurchaseReturn),
		Reference: newReturn.ReturnNumber, WarehouseID: wh.ID,
	})
	require.NoError(t, err)

	// Legacy path: a consumption line keyed on the return id with reason purchase_return.
	cons, err := client.Consumption.Create().SetTenantID(tenantID).SetOrderID(legacyReturn.ID).
		SetItems([]schema.ConsumptionItemJSON{}).SetReason("purchase_return").SetProcessedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	_, err = client.ConsumptionLine.Create().SetTenantID(tenantID).SetConsumptionID(cons.ID).
		SetOrderID(legacyReturn.ID).SetWarehouseID(wh.ID).SetFinishedItemSku(it.Sku).
		SetIngredientItemID(it.ID).SetIngredientSku(it.Sku).SetQuantity(1).
		SetReason("purchase_return").SetConsumedAt(time.Now().Add(-time.Hour)).Save(ctx)
	require.NoError(t, err)

	res, err := svc.ItemStockHistory(ctx, tenantID, it.Sku, StockHistoryFilter{})
	require.NoError(t, err)
	require.Len(t, res.Movements, 2)
	refs := map[string]MovementRow{}
	for _, mv := range res.Movements {
		require.Equal(t, "purchase_return", mv.Type, "a supplier return must never read as a sale")
		require.Equal(t, "Purchase Return", mv.Label)
		require.Equal(t, -1.0, mv.QuantityChange)
		require.Equal(t, "ELECTROMATE", mv.Counterparty)
		refs[mv.Reference] = mv
	}
	require.Contains(t, refs, "PRET-TEST-NEW")
	require.Contains(t, refs, "PRET-TEST-OLD")
	require.Equal(t, 2.0, res.Summary.TotalPurchaseReturns)
	require.Equal(t, 0.0, res.Summary.TotalSold)

	// No valued stock.adjusted event for a purchase return: the credit note carries the GL.
	n, err := client.OutboxEvent.Query().
		Where(outboxevent.TenantID(tenantID), outboxevent.EventType("inventory.stock.adjusted")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}
