package items

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/bundle"
	"github.com/bengobox/inventory-service/internal/ent/bundlecomponent"
	"github.com/bengobox/inventory-service/internal/ent/customfieldvalue"
	"github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/inventoryserial"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
	"github.com/bengobox/inventory-service/internal/ent/itemconsumptiondaily"
	"github.com/bengobox/inventory-service/internal/ent/itempricing"
	"github.com/bengobox/inventory-service/internal/ent/itemtranslation"
	"github.com/bengobox/inventory-service/internal/ent/itemvariant"
	"github.com/bengobox/inventory-service/internal/ent/modifiergroup"
	"github.com/bengobox/inventory-service/internal/ent/modifieroption"
	"github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	"github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/requisitionline"
	"github.com/bengobox/inventory-service/internal/ent/rfqline"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/stockcountline"
	"github.com/bengobox/inventory-service/internal/ent/stocklevelevent"
	"github.com/bengobox/inventory-service/internal/ent/stocktransferline"
	"github.com/bengobox/inventory-service/internal/ent/warranty"
)

// DefaultEOLRetentionDays is the default End-of-Life retention window: an item marked EOL is
// hard-deleted by the purge scheduler once its end_of_life_at is older than this (configurable
// via EOL_RETENTION_DAYS).
const DefaultEOLRetentionDays = 7

// MarkItemEOL marks an item End-of-Life by SKU. In a single transaction it sets
// end_of_life_at=now AND is_active=false — the latter makes the item disappear immediately from
// item listings, the POS live catalog (fetched with status=active), and ordering — then emits an
// enriched `inventory.item.updated` outbox event so the pos-api / ordering catalog consumers
// project the change (flip pos_catalog_override.is_available=false and bump the POS catalog
// version fingerprint so terminals refresh). Idempotent: re-marking an already-EOL item just
// refreshes the timestamp. Returns a not-found error when the SKU doesn't exist for the tenant.
func (s *Service) MarkItemEOL(ctx context.Context, tenantID uuid.UUID, sku string) (*ItemDTO, error) {
	return s.setItemEOL(ctx, tenantID, sku, true)
}

// RestoreItemEOL un-marks an EOL item by SKU: clears end_of_life_at and re-activates it
// (is_active=true) in a transaction, then emits `inventory.item.updated` so the item reappears in
// listings and the POS/ordering catalog. Returns a not-found error when the SKU doesn't exist.
func (s *Service) RestoreItemEOL(ctx context.Context, tenantID uuid.UUID, sku string) (*ItemDTO, error) {
	return s.setItemEOL(ctx, tenantID, sku, false)
}

// setItemEOL is the shared mark/restore transaction.
func (s *Service) setItemEOL(ctx context.Context, tenantID uuid.UUID, sku string, eol bool) (dto *ItemDTO, err error) {
	existing, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.Sku(sku)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: item not found")
		}
		return nil, fmt.Errorf("items: query item: %w", err)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	upd := tx.Item.UpdateOneID(existing.ID)
	if eol {
		upd = upd.SetEndOfLifeAt(time.Now().UTC()).SetIsActive(false)
	} else {
		upd = upd.ClearEndOfLifeAt().SetIsActive(true)
	}
	i, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: update EOL flag: %w", err)
	}

	if err = s.emitItemUpdatedEvent(ctx, tx, tenantID, i); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit transaction: %w", err)
	}
	return s.mapToDTO(i), nil
}

// emitItemUpdatedEvent writes an enriched `inventory.item.updated` outbox event inside tx,
// mirroring the payload UpdateItem publishes (plus end_of_life_at) so downstream POS/ordering
// catalog consumers stay in sync. Kept here so the EOL mutations reuse the exact same event shape
// without duplicating UpdateItem's large body.
func (s *Service) emitItemUpdatedEvent(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, i *ent.Item) error {
	categoryName := ""
	if i.CategoryID != nil {
		if cat, catErr := s.client.ItemCategory.Get(ctx, *i.CategoryID); catErr == nil {
			categoryName = cat.Name
		}
	}
	unitName, unitAbbrev, unitKraQty := "", "", ""
	if i.UnitID != nil {
		if u, uErr := s.client.Unit.Get(ctx, *i.UnitID); uErr == nil {
			unitName = u.Name
			unitAbbrev = u.Abbreviation
			unitKraQty = u.KraQtyUnitCd
		}
	}

	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "inventory",
		AggregateID:   i.ID,
		EventType:     "item.updated",
		Payload: map[string]any{
			"id":                        i.ID,
			"sku":                       i.Sku,
			"name":                      i.Name,
			"description":               i.Description,
			"type":                      i.Type,
			"category_id":               i.CategoryID,
			"category_name":             categoryName,
			"manufacturer":              i.Manufacturer,
			"model":                     i.Model,
			"unit_id":                   i.UnitID,
			"unit_name":                 unitName,
			"is_active":                 i.IsActive,
			"end_of_life_at":            i.EndOfLifeAt,
			"image_url":                 i.ImageURL,
			"tags":                      i.Tags,
			"barcode":                   i.Barcode,
			"barcode_type":              i.BarcodeType,
			"requires_age_verification": i.RequiresAgeVerification,
			"is_controlled_substance":   i.IsControlledSubstance,
			"is_perishable":             i.IsPerishable,
			"track_serial_numbers":      i.TrackSerialNumbers,
			"track_lots":                i.TrackLots,
			"weight_kg":                 i.WeightKg,
			"dimensions_cm":             i.DimensionsCm,
			"duration_minutes":          i.DurationMinutes,
			"use_case":                  i.UseCase,
			"meal_plan":                 i.MealPlan,
			"occupancy_basis":           i.OccupancyBasis,
			"max_adults":                i.MaxAdults,
			"max_children":              i.MaxChildren,
			"tax_code_id":               i.TaxCodeID,
			"tax_inclusive":             i.TaxInclusive,
			"cost_price":                i.CostPrice,
			// Selling-price guardrails — the ceiling (max_selling_price) also doubles as an
			// item's effective default price when it has no dedicated pricing-tier row (see
			// pricing_enrich.go effectivePrice). Consumers that want the real customer-facing
			// price (treasury's eTIMS item mirror, any future price-aware subscriber) need these
			// on every item.updated event, not just cost_price.
			"min_selling_price":         i.MinSellingPrice,
			"max_selling_price":         i.MaxSellingPrice,
			"selling_price":             i.MaxSellingPrice,
			"unit_content_qty":          i.UnitContentQty,
			"unit_content_uom":          i.UnitContentUom,
			"stock_tracking_mode":       i.StockTrackingMode,
		},
		Timestamp: time.Now().UTC(),
	}
	mergeEtimsEventFields(event.Payload, i, unitAbbrev, unitKraQty)

	payload, err := event.ToJSON()
	if err != nil {
		return fmt.Errorf("items: marshal EOL event: %w", err)
	}
	_, err = tx.OutboxEvent.Create().
		SetID(event.ID).
		SetTenantID(tenantID).
		SetAggregateType(event.AggregateType).
		SetAggregateID(event.AggregateID.String()).
		SetEventType(event.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(event.Timestamp).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("items: create EOL outbox record: %w", err)
	}
	return nil
}

// SetCostPriceAndPublish updates an item's cost_price (used by recipes.Service.RecalculateRecipeCosts
// to write a RECIPE's computed cost_per_portion through onto its owning Item, so the recipe's real
// ingredient cost reaches every consumer that already trusts Item.CostPrice — the sale-time COGS
// journal, P&L cost-of-goods, the profitability report, and the reversal tool's cost lookup — none
// of which need any change of their own. Emits the exact same enriched inventory.item.updated event
// UpdateItem publishes (via emitItemUpdatedEvent) so the POS/ordering catalog sync picks it up too.
// A no-op (returns nil) when the price is unchanged, to avoid a pointless event on every unrelated
// recipe recompute.
func (s *Service) SetCostPriceAndPublish(ctx context.Context, tenantID, itemID uuid.UUID, costPrice float64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin cost-price tx: %w", err)
	}
	current, err := tx.Item.Query().Where(item.ID(itemID), item.TenantID(tenantID)).Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: load item for cost-price update: %w", err)
	}
	if current.CostPrice != nil && *current.CostPrice == costPrice {
		_ = tx.Rollback()
		return nil
	}
	updated, err := tx.Item.UpdateOneID(itemID).SetCostPrice(costPrice).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: set cost price: %w", err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: emit cost-price update event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("items: commit cost-price update: %w", err)
	}
	return nil
}

// PublishItemUpdatedEvent re-emits the enriched inventory.item.updated event for an item as it
// currently stands in the DB — the exported entry point other modules (recipes.Service, wired via
// WithItemsService) use to notify POS/ordering catalog sync of a change they made directly (e.g. a
// RECIPE's price correction, which lives on the Recipe row, not Item) without duplicating the
// event-construction logic in emitItemUpdatedEvent.
func (s *Service) PublishItemUpdatedEvent(ctx context.Context, tenantID, itemID uuid.UUID) error {
	itm, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.ID(itemID)).Only(ctx)
	if err != nil {
		return fmt.Errorf("items: load item %s for event publish: %w", itemID, err)
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin tx for event publish: %w", err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, itm); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// PurgeExpiredEOL hard-deletes items whose end_of_life_at is older than retentionDays, across ALL
// tenants (each item carries its own tenant_id, so this stays tenant-generic). Returns the number
// of items purged and the number skipped.
//
// CRITICAL (audit-trail safety): items are hard-deleted only when they carry NO transactional or
// usage history — no purchase-order / goods-receipt / adjustment lines, stock-level events,
// transfer / return / requisition / RFQ / stock-count lines, serials, daily-consumption rows, and
// they are not referenced as an ingredient in another recipe or as a component in another bundle.
// Any such item is SKIPPED and logged (it stays EOL/inactive, i.e. still hidden) so the
// finance/eTIMS audit trail is preserved. Owned catalog children (balances, pricing, translations,
// assets, custom-field values, warranties, lots, this item's own modifier groups/options,
// variants, bundle, and produced recipe) are removed in the same transaction before the item. Any
// unexpected FK failure rolls back and skips the item rather than aborting the whole run.
func (s *Service) PurgeExpiredEOL(ctx context.Context, retentionDays int) (purged, skipped int, err error) {
	if retentionDays <= 0 {
		retentionDays = DefaultEOLRetentionDays
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	rows, err := s.client.Item.Query().
		Where(item.EndOfLifeAtNotNil(), item.EndOfLifeAtLT(cutoff)).
		All(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("items: query expired EOL items: %w", err)
	}

	for _, it := range rows {
		if blocked, reason := s.eolHasBlockingHistory(ctx, it.ID); blocked {
			skipped++
			s.log.Info("eol purge: skipped item with transactional history (kept EOL for audit)",
				zap.String("tenant", it.TenantID.String()),
				zap.String("sku", it.Sku),
				zap.String("item_id", it.ID.String()),
				zap.String("reason", reason))
			continue
		}
		if derr := s.hardDeleteItem(ctx, it.ID); derr != nil {
			skipped++
			s.log.Warn("eol purge: hard-delete failed, skipped (item stays EOL)",
				zap.String("tenant", it.TenantID.String()),
				zap.String("sku", it.Sku),
				zap.String("item_id", it.ID.String()),
				zap.Error(derr))
			continue
		}
		purged++
		s.log.Info("eol purge: item hard-deleted",
			zap.String("tenant", it.TenantID.String()),
			zap.String("sku", it.Sku),
			zap.String("item_id", it.ID.String()))
	}

	if purged > 0 || skipped > 0 {
		s.log.Info("eol purge complete", zap.Int("purged", purged), zap.Int("skipped", skipped), zap.Int("retention_days", retentionDays))
	}
	return purged, skipped, nil
}

// eolHasBlockingHistory reports whether the item is referenced by any transactional / usage record
// that must be preserved for the finance/eTIMS audit trail (or that would corrupt another item's
// recipe/bundle). Returns a short reason for logging.
func (s *Service) eolHasBlockingHistory(ctx context.Context, id uuid.UUID) (bool, string) {
	checks := []struct {
		name  string
		exist func() (bool, error)
	}{
		{"purchase_order_line", func() (bool, error) { return s.client.PurchaseOrderLine.Query().Where(purchaseorderline.ItemID(id)).Exist(ctx) }},
		{"goods_receipt_line", func() (bool, error) { return s.client.GoodsReceiptLine.Query().Where(goodsreceiptline.ItemID(id)).Exist(ctx) }},
		{"stock_adjustment", func() (bool, error) { return s.client.StockAdjustment.Query().Where(stockadjustment.ItemID(id)).Exist(ctx) }},
		{"stock_level_event", func() (bool, error) { return s.client.StockLevelEvent.Query().Where(stocklevelevent.ItemID(id)).Exist(ctx) }},
		{"purchase_return_line", func() (bool, error) { return s.client.PurchaseReturnLine.Query().Where(purchasereturnline.ItemID(id)).Exist(ctx) }},
		{"stock_transfer_line", func() (bool, error) { return s.client.StockTransferLine.Query().Where(stocktransferline.ItemID(id)).Exist(ctx) }},
		{"requisition_line", func() (bool, error) { return s.client.RequisitionLine.Query().Where(requisitionline.ItemID(id)).Exist(ctx) }},
		{"rfq_line", func() (bool, error) { return s.client.RFQLine.Query().Where(rfqline.ItemID(id)).Exist(ctx) }},
		{"stock_count_line", func() (bool, error) { return s.client.StockCountLine.Query().Where(stockcountline.ItemID(id)).Exist(ctx) }},
		{"item_consumption_daily", func() (bool, error) { return s.client.ItemConsumptionDaily.Query().Where(itemconsumptiondaily.ItemID(id)).Exist(ctx) }},
		{"inventory_serial", func() (bool, error) { return s.client.InventorySerial.Query().Where(inventoryserial.ItemID(id)).Exist(ctx) }},
		{"used_as_recipe_ingredient", func() (bool, error) { return s.client.RecipeIngredient.Query().Where(recipeingredient.ItemID(id)).Exist(ctx) }},
		{"used_as_bundle_component", func() (bool, error) { return s.client.BundleComponent.Query().Where(bundlecomponent.ComponentItemID(id)).Exist(ctx) }},
	}
	for _, c := range checks {
		ok, qerr := c.exist()
		if qerr != nil {
			// On a query error, err on the side of caution: treat as blocking so we never
			// delete an item whose history we could not verify.
			return true, c.name + " (check error)"
		}
		if ok {
			return true, c.name
		}
	}
	return false, ""
}

// hardDeleteItem removes an item and its OWNED catalog children in a single transaction. Callers
// must have confirmed the item has no blocking history (see eolHasBlockingHistory).
func (s *Service) hardDeleteItem(ctx context.Context, id uuid.UUID) (err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Item's own modifier groups + their options.
	groupIDs, gerr := tx.ModifierGroup.Query().Where(modifiergroup.ItemID(id)).IDs(ctx)
	if gerr != nil {
		err = gerr
		return err
	}
	if len(groupIDs) > 0 {
		if _, err = tx.ModifierOption.Delete().Where(modifieroption.GroupIDIn(groupIDs...)).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.ModifierGroup.Delete().Where(modifiergroup.IDIn(groupIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// Item's own bundle (if it is a bundle) + its component rows.
	bundleIDs, berr := tx.Bundle.Query().Where(bundle.ItemID(id)).IDs(ctx)
	if berr != nil {
		err = berr
		return err
	}
	if len(bundleIDs) > 0 {
		if _, err = tx.BundleComponent.Delete().Where(bundlecomponent.BundleIDIn(bundleIDs...)).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.Bundle.Delete().Where(bundle.IDIn(bundleIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// Recipe produced by this item + its ingredient lines (only the recipe this item OWNS; usage
	// of the item as an ingredient elsewhere is a blocking check, handled upstream).
	recipeIDs, rerr := tx.Recipe.Query().Where(recipe.ItemID(id)).IDs(ctx)
	if rerr != nil {
		err = rerr
		return err
	}
	if len(recipeIDs) > 0 {
		if _, err = tx.RecipeIngredient.Delete().Where(recipeingredient.RecipeIDIn(recipeIDs...)).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.Recipe.Delete().Where(recipe.IDIn(recipeIDs...)).Exec(ctx); err != nil {
			return err
		}
	}

	// Flat owned children keyed directly by item_id.
	if _, err = tx.InventoryBalance.Delete().Where(inventorybalance.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.ItemPricing.Delete().Where(itempricing.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.ItemTranslation.Delete().Where(itemtranslation.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.ItemAsset.Delete().Where(itemasset.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.CustomFieldValue.Delete().Where(customfieldvalue.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.Warranty.Delete().Where(warranty.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.InventoryLot.Delete().Where(inventorylot.ItemID(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err = tx.ItemVariant.Delete().Where(itemvariant.ItemID(id)).Exec(ctx); err != nil {
		return err
	}

	// Finally the item itself.
	if err = tx.Item.DeleteOneID(id).Exec(ctx); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
