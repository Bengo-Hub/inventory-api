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

// seedBalances creates InventoryBalance rows for INGREDIENT and GOODS items only.
// SERVICE items (events) are non-stockable and are excluded.
func seedBalances(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	wh, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.Code("MAIN")).
		Only(ctx)
	if ent.IsNotFound(err) {
		wh, err = client.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.IsDefault(true)).
			First(ctx)
	}
	if err != nil {
		return fmt.Errorf("find warehouse: %w", err)
	}

	for _, def := range catalogItemDefs {
		// SERVICE items (events, experiences) are non-stockable — skip.
		if def.ItemType == entitem.TypeSERVICE {
			continue
		}

		id := itemUUID(tenantID, def.SKU)

		itm, err := client.Item.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("find item %s: %w", def.SKU, err)
		}

		exists, err := client.InventoryBalance.Query().
			Where(
				entinvbal.TenantID(tenantID),
				entinvbal.ItemID(itm.ID),
				entinvbal.WarehouseID(wh.ID),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check balance for %s: %w", def.SKU, err)
		}
		if exists {
			continue
		}

		if _, err := client.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itm.ID).
			SetWarehouseID(wh.ID).
			SetOnHand(def.OnHand).
			SetAvailable(def.OnHand).
			SetReserved(0).
			SetReorderLevel(1).
			SetUnitOfMeasure(def.UnitName).
			SetUpdatedAt(time.Now()).
			Save(ctx); err != nil {
			log.Printf("balance for %s: %v (may already exist)", def.SKU, err)
			continue
		}
		log.Printf("balance created: %s on_hand=%d", def.SKU, def.OnHand)
	}
	return nil
}
