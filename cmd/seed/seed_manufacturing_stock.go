package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// seedManufacturingStock mirrors the detergent demo stock into the manufacturing
// outlet's warehouse (code "MFG") once that warehouse has been synced from
// auth-api's demo-manufacturing outlet. It is a safe no-op (never fails) when the
// warehouse isn't present yet, so seed ordering across services doesn't matter —
// re-running the seed after the branch sync populates the stock.
func seedManufacturingStock(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	wh, err := client.Warehouse.Query().
		Where(
			entwarehouse.TenantID(tenantID),
			entwarehouse.Code("MFG"),
			entwarehouse.OutletIDNotNil(),
		).
		First(ctx)
	if ent.IsNotFound(err) {
		log.Printf("[SKIP] manufacturing stock: MFG warehouse not synced yet for tenant %s", tenantID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("find MFG warehouse: %w", err)
	}

	created := 0
	for _, def := range detergentItems() {
		itm, e := client.Item.Get(ctx, itemUUID(tenantID, def.SKU))
		if e != nil {
			continue
		}
		exists, _ := client.InventoryBalance.Query().
			Where(entinvbal.TenantID(tenantID), entinvbal.ItemID(itm.ID), entinvbal.WarehouseID(wh.ID)).
			Exist(ctx)
		if exists {
			continue
		}
		// Reorder thresholds chosen so a few low-volume chemicals fall below them,
		// demonstrating the manufacturing dashboard's low raw-material alerts.
		reorder := 20
		if def.ItemType == entitem.TypeINGREDIENT {
			reorder = 100
		}
		if _, e := client.InventoryBalance.Create().
			SetTenantID(tenantID).SetItemID(itm.ID).SetWarehouseID(wh.ID).
			SetOnHand(float64(def.OnHand)).SetAvailable(float64(def.OnHand)).SetReserved(0).
			SetReorderLevel(reorder).SetReorderQuantity(reorder).
			SetUnitOfMeasure(def.UnitName).SetUpdatedAt(time.Now()).
			Save(ctx); e != nil {
			log.Printf("[WARN] mfg stock for %s: %v", def.SKU, e)
			continue
		}
		created++
	}
	log.Printf("manufacturing stock mirrored into MFG warehouse for tenant %s (%d balances)", tenantID, created)
	return nil
}
