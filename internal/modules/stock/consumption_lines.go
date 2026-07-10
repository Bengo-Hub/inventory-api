package stock

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
)

// consumptionLineInput carries everything needed to persist one ConsumptionLine row and
// fold it into the day-grain rollup, gathered by RecordConsumption while it already has
// the item/balance/cost context in hand — see explodedIngredient.RecipeID/RecipeSKU in
// bom.go for where the recipe attribution this restores originally comes from.
type consumptionLineInput struct {
	consumptionID    uuid.UUID
	orderID          uuid.UUID
	warehouseID      uuid.UUID
	outletID         *uuid.UUID
	recipeID         uuid.UUID // uuid.Nil when the ingredient was sold directly (no recipe)
	recipeSKU        string
	finishedItemSKU  string
	ingredientItemID uuid.UUID
	ingredientSKU    string
	quantity         float64
	unitCost         float64
	theoretical      bool
	reason           string
	consumedAt       time.Time
}

// recordConsumptionLine persists a normalized ConsumptionLine row and folds it into the
// same day's ItemConsumptionDaily rollup — both inside the caller's transaction, so the
// ingredient utilization report is exactly as fresh as the balance update it accompanies.
// Best-effort: a failure here is logged, never propagated — this is report data, the sale
// itself already succeeded via the balance/Consumption writes RecordConsumption already did.
func (s *Service) recordConsumptionLine(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, in consumptionLineInput) {
	totalCost := round4(in.quantity * in.unitCost)

	create := tx.ConsumptionLine.Create().
		SetTenantID(tenantID).
		SetConsumptionID(in.consumptionID).
		SetOrderID(in.orderID).
		SetWarehouseID(in.warehouseID).
		SetFinishedItemSku(in.finishedItemSKU).
		SetIngredientItemID(in.ingredientItemID).
		SetIngredientSku(in.ingredientSKU).
		SetQuantity(in.quantity).
		SetUnitCost(in.unitCost).
		SetTotalCost(totalCost).
		SetTheoretical(in.theoretical).
		SetConsumedAt(in.consumedAt)

	if in.outletID != nil {
		create.SetOutletID(*in.outletID)
	}
	if in.recipeID != uuid.Nil {
		create.SetRecipeID(in.recipeID)
	}
	if in.recipeSKU != "" {
		create.SetRecipeSku(in.recipeSKU)
	}
	if in.reason != "" {
		create.SetReason(in.reason)
	}

	if _, err := create.Save(ctx); err != nil {
		s.log.Warn("consumption line: persist failed (report data only, sale unaffected)",
			zap.String("ingredient_sku", in.ingredientSKU), zap.Error(err))
		return
	}

	s.upsertDailyRollup(ctx, tx, tenantID, in, totalCost)
}

// upsertDailyRollup increments (or creates) today's ItemConsumptionDaily bucket for this
// (warehouse, item, recipe) combination. The rollup is a query accelerator, never the
// source of truth — ConsumptionLine is — so a failure here must not fail the sale.
func (s *Service) upsertDailyRollup(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, in consumptionLineInput, totalCost float64) {
	bucket := in.consumedAt.UTC().Truncate(24 * time.Hour)

	create := tx.ItemConsumptionDaily.Create().
		SetTenantID(tenantID).
		SetWarehouseID(in.warehouseID).
		SetItemID(in.ingredientItemID).
		SetItemSku(in.ingredientSKU).
		SetRecipeID(in.recipeID).
		SetRecipeSku(in.recipeSKU).
		SetBucketDate(bucket).
		SetQuantity(in.quantity).
		SetTotalCost(totalCost)
	if in.outletID != nil {
		create.SetOutletID(*in.outletID)
	}

	err := create.
		OnConflictColumns("tenant_id", "warehouse_id", "item_id", "recipe_id", "bucket_date").
		Update(func(u *ent.ItemConsumptionDailyUpsert) {
			u.AddQuantity(in.quantity)
			u.AddTotalCost(totalCost)
			u.SetUpdatedAt(time.Now())
		}).
		Exec(ctx)
	if err != nil {
		s.log.Warn("consumption daily rollup: upsert failed (report data only, sale unaffected)",
			zap.String("ingredient_sku", in.ingredientSKU), zap.Error(err))
	}
}

// itemCostPrice safely dereferences Item.CostPrice (nil when never set/priced — e.g. a
// non-billable accompaniment), so callers building a consumptionLineInput don't each need
// their own nil check.
func itemCostPrice(itm *ent.Item) float64 {
	if itm == nil || itm.CostPrice == nil {
		return 0
	}
	return *itm.CostPrice
}

// resolveOutletID parses the string outlet id already resolved by outletIDForWarehouse
// (cascade.go) into a *uuid.UUID, nil when unresolved — avoids duplicating the
// warehouse→outlet lookup that function already owns.
func (s *Service) resolveOutletID(ctx context.Context, tx *ent.Tx, warehouseID uuid.UUID) *uuid.UUID {
	str := s.outletIDForWarehouse(ctx, tx, warehouseID)
	if str == "" {
		return nil
	}
	if oid, err := uuid.Parse(str); err == nil {
		return &oid
	}
	return nil
}

// persistStockLevelEvent records a low/out/restocked transition so the ingredient
// utilization report can phase-band a consumption trend. Mirrors the outbox event
// checkAndPublishLowStock already publishes — same trigger, now also a queryable row.
func (s *Service) persistStockLevelEvent(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, outletID *uuid.UUID, eventType string, onHand float64, reorderLevel int) {
	create := tx.StockLevelEvent.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetWarehouseID(warehouseID).
		SetOnHandAtEvent(onHand).
		SetReorderLevelAtEvent(float64(reorderLevel))
	switch eventType {
	case "low":
		create.SetEventType("low")
	case "out":
		create.SetEventType("out")
	case "restocked":
		create.SetEventType("restocked")
	default:
		return
	}
	if outletID != nil {
		create.SetOutletID(*outletID)
	}
	if _, err := create.Save(ctx); err != nil {
		s.log.Warn("stock level event: persist failed (report data only)",
			zap.String("item_id", itemID.String()), zap.String("event_type", eventType), zap.Error(err))
	}
}
