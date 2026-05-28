package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/http/handlers"
)

// seedBalances creates or skips InventoryBalance rows for all seeded items.
// SERVICE items receive a zero-quantity balance so they participate in outlet-scoped queries.
func seedBalances(ctx context.Context, client *ent.Client, tenantID uuid.UUID, slug string) error {
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

	// Load unit reorder defaults from tenant config; fall back to built-in defaults.
	unitDefaults := handlers.DefaultUnitReorderLevels()
	globalDefault := 10
	cfg, cfgErr := client.TenantInventoryConfig.Query().
		Where(entconfig.TenantID(tenantID)).
		Only(ctx)
	if cfgErr == nil {
		if len(cfg.UnitReorderDefaults) > 0 {
			unitDefaults = cfg.UnitReorderDefaults
		}
		if cfg.DefaultReorderLevel > 0 {
			globalDefault = cfg.DefaultReorderLevel
		}
	}

	// Build unit name → abbreviation map for reorder level lookup.
	unitAbbr := make(map[string]string, len(unitDefs))
	for _, u := range unitDefs {
		unitAbbr[u.Name] = u.Abbreviation
	}

	for _, def := range itemDefsForSlug(slug) {
		id := itemUUID(tenantID, def.SKU)

		itm, err := client.Item.Get(ctx, id)
		if err != nil {
			if ent.IsNotFound(err) {
				log.Printf("[SKIP] balance: item %s not found, run seedItems first", def.SKU)
				continue
			}
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

		onHand := def.OnHand
		reorderLvl := 0

		if def.ItemType != entitem.TypeSERVICE {
			// Resolve reorder level from unit abbreviation; fall back to global default.
			abbr := strings.ToLower(unitAbbr[def.UnitName])
			if v, ok := unitDefaults[abbr]; ok && v > 0 {
				reorderLvl = v
			} else {
				reorderLvl = globalDefault
			}
		}

		if _, err := client.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itm.ID).
			SetWarehouseID(wh.ID).
			SetOnHand(onHand).
			SetAvailable(onHand).
			SetReserved(0).
			SetReorderLevel(reorderLvl).
			SetReorderQuantity(reorderLvl).
			SetUnitOfMeasure(def.UnitName).
			SetUpdatedAt(time.Now()).
			Save(ctx); err != nil {
			log.Printf("balance for %s: %v (may already exist)", def.SKU, err)
			continue
		}
		log.Printf("balance created: %s on_hand=%d reorder_level=%d", def.SKU, onHand, reorderLvl)
	}
	return nil
}
