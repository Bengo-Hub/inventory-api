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
//
// Stock is routed to the warehouse whose use_case matches the item group's use_case (retail
// goods → the retail outlet's warehouse, pharmacy → pharmacy, salon retail → services),
// falling back to the MAIN/default warehouse for food/conference/etc. Dumping everything into
// the default (hotel) warehouse made items.ListItems' outlet separation hide every retail/
// pharmacy GOODS from its own outlet — goods stocked EXCLUSIVELY in another outlet's
// warehouse are excluded, so the demo supermarket POS showed an empty catalog.
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

	// Warehouse per use_case (first match wins) for routing item groups to their outlet.
	allWh, err := client.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list warehouses: %w", err)
	}
	whByUseCase := make(map[string]*ent.Warehouse, len(allWh))
	for _, w := range allWh {
		uc := strings.ToLower(strings.TrimSpace(w.UseCase))
		if uc == "" {
			continue
		}
		if _, ok := whByUseCase[uc]; !ok {
			whByUseCase[uc] = w
		}
	}
	ucBySKU := useCaseBySKU(slug)
	targetWarehouse := func(sku string) *ent.Warehouse {
		var uc string
		switch ucBySKU[sku] {
		case entitem.UseCaseRETAIL:
			uc = "retail"
		case entitem.UseCasePHARMACY:
			uc = "pharmacy"
		case entitem.UseCaseSALON_SERVICE:
			uc = "services"
		default:
			return wh
		}
		if w, ok := whByUseCase[uc]; ok {
			return w
		}
		return wh
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

		targetWh := targetWarehouse(def.SKU)
		// Earlier seed runs parked outlet-specific goods in the default warehouse; a stale
		// EMPTY row there keeps outlet separation hiding the item from its real outlet, so
		// drop zero-quantity rows sitting in the wrong warehouse before (re)seeding.
		if targetWh.ID != wh.ID {
			if n, derr := client.InventoryBalance.Delete().
				Where(
					entinvbal.TenantID(tenantID),
					entinvbal.ItemID(itm.ID),
					entinvbal.WarehouseIDNEQ(targetWh.ID),
					entinvbal.OnHand(0),
				).
				Exec(ctx); derr == nil && n > 0 {
				log.Printf("balance relocated: %s — %d stale empty row(s) removed", def.SKU, n)
			}
		}

		existing, err := client.InventoryBalance.Query().
			Where(
				entinvbal.TenantID(tenantID),
				entinvbal.ItemID(itm.ID),
				entinvbal.WarehouseID(targetWh.ID),
			).
			First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("check balance for %s: %w", def.SKU, err)
		}
		if existing != nil {
			// Re-seed opening stock on a drained row (a demo-data wipe zeroes balances) so
			// fresh test rounds start with sellable quantities. Rows with live stock are
			// never touched.
			if existing.OnHand <= 0 && def.OnHand > 0 {
				if _, uerr := existing.Update().
					SetOnHand(float64(def.OnHand)).
					SetAvailable(float64(def.OnHand)).
					SetReserved(0).
					SetUpdatedAt(time.Now()).
					Save(ctx); uerr == nil {
					log.Printf("balance restocked: %s @ %s on_hand=%d", def.SKU, targetWh.Name, def.OnHand)
				}
			}
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
			SetWarehouseID(targetWh.ID).
			SetOnHand(float64(onHand)).
			SetAvailable(float64(onHand)).
			SetReserved(0).
			SetReorderLevel(reorderLvl).
			SetReorderQuantity(reorderLvl).
			SetUnitOfMeasure(def.UnitName).
			SetUpdatedAt(time.Now()).
			Save(ctx); err != nil {
			log.Printf("balance for %s: %v (may already exist)", def.SKU, err)
			continue
		}
		log.Printf("balance created: %s @ %s on_hand=%d reorder_level=%d", def.SKU, targetWh.Name, onHand, reorderLvl)
	}
	return nil
}
