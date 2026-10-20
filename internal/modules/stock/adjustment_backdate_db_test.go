package stock

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// A backdated adjustment is stored under its effective date and lands among the older rows of the
// ledger and the Adjustments list instead of on top, keeps its real entry time, books on the
// effective day, and a bad date changes nothing. Requires a real local Postgres via POSTGRES_URL;
// skipped when unset.
func TestAdjustStock_BackdatedLandsOnItsDate(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping DB-backed backdated adjustment test")
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
		_, _ = client.StockAdjustment.Delete().Where(stockadjustment.TenantID(tenantID)).Exec(ctx)
		_, _ = client.InventoryBalance.Delete().Where(inventorybalance.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Item.Delete().Where(item.TenantID(tenantID)).Exec(ctx)
		_, _ = client.Warehouse.Delete().Where(warehouse.TenantID(tenantID)).Exec(ctx)
		_, _ = client.OutboxEvent.Delete().Where(outboxevent.TenantID(tenantID)).Exec(ctx)
		_ = client.Tenant.DeleteOneID(tenantID).Exec(ctx)
	})
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Backdate Test").
		SetSlug("backdate-test-" + tenantID.String()).Save(ctx)
	require.NoError(t, err)
	wh, err := client.Warehouse.Create().SetTenantID(tenantID).SetName("Main Store").SetCode("MAIN").
		SetIsDefault(true).Save(ctx)
	require.NoError(t, err)
	it, err := client.Item.Create().SetTenantID(tenantID).SetSku("BD-TEST-1").SetName("Rice 1kg").
		SetCostPrice(100).Save(ctx)
	require.NoError(t, err)
	_, err = client.InventoryBalance.Create().SetTenantID(tenantID).SetItemID(it.ID).SetWarehouseID(wh.ID).
		SetOnHand(76).SetAvailable(76).Save(ctx)
	require.NoError(t, err)
	svc := NewService(client, zap.NewNop())

	back := time.Now().UTC().AddDate(0, 0, -10)
	day := time.Date(back.Year(), back.Month(), back.Day(), 0, 0, 0, 0, time.UTC)

	// A bad date is refused before any stock moves.
	for _, bad := range []string{time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02"), "yesterday"} {
		_, err = svc.AdjustStock(ctx, tenantID, AdjustStockRequest{
			SKU: it.Sku, Adjustment: 5, Reason: "found", WarehouseID: wh.ID, AdjustmentDate: bad,
		})
		require.ErrorIs(t, err, ErrInvalidAdjustmentDate)
	}
	n, err := client.StockAdjustment.Query().Where(stockadjustment.TenantID(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "a refused date must not write an adjustment")

	// The backdated one first (+10), then a normal one (-1) entered afterwards.
	res, err := svc.AdjustStock(ctx, tenantID, AdjustStockRequest{
		SKU: it.Sku, Adjustment: 10, Reason: "found", WarehouseID: wh.ID,
		AdjustmentDate: day.Format("2006-01-02"), Reference: "ADJ-BACK",
	})
	require.NoError(t, err)
	require.True(t, res.AdjustedAt.Equal(day), "response carries the effective date, got %v", res.AdjustedAt)
	_, err = svc.AdjustStock(ctx, tenantID, AdjustStockRequest{
		SKU: it.Sku, Adjustment: -1, Reason: "correction", WarehouseID: wh.ID, Reference: "ADJ-NOW",
	})
	require.NoError(t, err)

	// Stored: adjusted_at is the effective day, created_at the real entry time.
	row, err := client.StockAdjustment.Query().
		Where(stockadjustment.TenantID(tenantID), stockadjustment.Reference("ADJ-BACK")).Only(ctx)
	require.NoError(t, err)
	require.True(t, row.AdjustedAt.Equal(day))
	require.WithinDuration(t, time.Now(), row.CreatedAt, time.Minute)
	require.True(t, IsBackdatedAdjustment(row.AdjustedAt, row.CreatedAt))

	// Books: the valued event carries the effective date so treasury posts it on that day.
	ev, err := client.OutboxEvent.Query().
		Where(outboxevent.TenantID(tenantID), outboxevent.EventType("stock.adjusted"),
			outboxevent.AggregateID(row.ID.String())).Only(ctx)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(ev.Payload, &payload))
	inner, _ := payload["payload"].(map[string]any) // events are wrapped in an envelope
	require.Equal(t, day.Format(time.RFC3339), inner["adjusted_at"])

	// Ledger: today's row on top, the backdated row below it on its own date with its entry note
	// and a running balance computed from its neighbours (86 = 76 + 10), not today's snapshot.
	hist, err := svc.ItemStockHistory(ctx, tenantID, it.Sku, StockHistoryFilter{})
	require.NoError(t, err)
	require.Len(t, hist.Movements, 2)
	require.Equal(t, "ADJ-NOW", hist.Movements[0].Reference)
	require.Equal(t, "ADJ-BACK", hist.Movements[1].Reference)
	require.True(t, hist.Movements[1].OccurredAt.Equal(day))
	require.NotNil(t, hist.Movements[1].EnteredAt, "the real entry time is kept as a note")
	require.Nil(t, hist.Movements[0].EnteredAt)
	require.NotNil(t, hist.Movements[1].QuantityAfter)
	require.Equal(t, 86.0, *hist.Movements[1].QuantityAfter)
	require.Equal(t, 85.0, *hist.Movements[0].QuantityAfter)

	// Adjustments list: same order.
	list, _, err := svc.ListAdjustments(ctx, tenantID, ListAdjustmentsRequest{})
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "ADJ-NOW", list[0].Reference)
	require.Equal(t, "ADJ-BACK", list[1].Reference)
}
