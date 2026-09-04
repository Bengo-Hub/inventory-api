package stock

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
)

// reversalReason marks compensating consumption records so they are excluded from
// reversible-quantity math and from being reversed themselves.
const reversalReason = "reversal"

// ReverseConsumptionItem selects one sale-line SKU to reverse. Quantity is the sale-line
// quantity being reversed and OfQuantity the total quantity of that SKU originally sold on
// the order — the ratio scales the recorded ingredient consumption (e.g. reversing 1 of 2
// plates reverses half of each ingredient line). OfQuantity <= 0 means the full ratio (1).
type ReverseConsumptionItem struct {
	SKU        string  `json:"sku"`
	Quantity   float64 `json:"quantity"`
	OfQuantity float64 `json:"of_quantity,omitempty"`
}

// ReverseConsumptionRequest reverses (part of) an order's recorded stock consumption:
// actually-deducted quantities go back to the warehouse balance, the utilization report
// (consumption lines + daily rollups) is compensated with negative entries, and a
// compensating Consumption row keeps the whole operation idempotent + auditable.
// Empty Items reverses the entire order consumption.
type ReverseConsumptionRequest struct {
	OrderID        uuid.UUID                `json:"order_id"`
	Items          []ReverseConsumptionItem `json:"items,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
}

// ReversedIngredient reports what one ingredient line's reversal actually did.
type ReversedIngredient struct {
	IngredientSKU string `json:"ingredient_sku"`
	RecipeSKU     string `json:"recipe_sku,omitempty"`
	// QuantityReversed is the utilization-report compensation (theoretical usage removed).
	QuantityReversed float64 `json:"quantity_reversed"`
	// StockReturned is what was actually added back on-hand — the deducted portion only
	// (shortfall/theoretical/unit-mismatch portions never left stock, so they never return).
	StockReturned float64 `json:"stock_returned"`
	CostReversed  float64 `json:"cost_reversed"`
}

// ReverseConsumptionResponse summarizes a consumption reversal.
type ReverseConsumptionResponse struct {
	ID                uuid.UUID            `json:"id"`
	OrderID           uuid.UUID            `json:"order_id"`
	Status            string               `json:"status"`
	AlreadyProcessed  bool                 `json:"already_processed,omitempty"`
	TotalCostReversed float64              `json:"total_cost_reversed"`
	Ingredients       []ReversedIngredient `json:"ingredients"`
}

// ReverseConsumption reverses recorded consumption for an order (whole or per sale-line SKU).
// It is the stock-side counterpart of a POS sale reversal: RecordConsumption deducted BOM
// ingredients net of shortfall, so the reversal returns exactly the deducted portion and
// compensates the utilization records for the full theoretical portion — mirroring what a
// manual repair would do, but idempotent (unique idempotency_key on the compensating
// Consumption row) and capped so repeated calls can never over-return stock.
func (s *Service) ReverseConsumption(ctx context.Context, tenantID uuid.UUID, req ReverseConsumptionRequest) (*ReverseConsumptionResponse, error) {
	if req.OrderID == uuid.Nil {
		return nil, fmt.Errorf("stock: reverse consumption: order_id required")
	}

	// Idempotent replay: the compensating Consumption row carries the caller's key.
	if req.IdempotencyKey != "" {
		existing, err := s.client.Consumption.Query().
			Where(entconsumption.IdempotencyKeyEQ(req.IdempotencyKey)).
			First(ctx)
		if err == nil {
			return &ReverseConsumptionResponse{
				ID:               existing.ID,
				OrderID:          existing.OrderID,
				Status:           existing.Status,
				AlreadyProcessed: true,
			}, nil
		}
	}

	// Original (non-reversal) consumptions for the order, and their per-recipe lines.
	originals, err := s.client.Consumption.Query().
		Where(
			entconsumption.TenantID(tenantID),
			entconsumption.OrderID(req.OrderID),
			entconsumption.ReasonNEQ(reversalReason),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: reverse consumption: load consumptions: %w", err)
	}
	if len(originals) == 0 {
		return nil, fmt.Errorf("stock: reverse consumption: no consumption recorded for order %s", req.OrderID)
	}

	allLines, err := s.client.ConsumptionLine.Query().
		Where(
			entconsumptionline.TenantID(tenantID),
			entconsumptionline.OrderID(req.OrderID),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: reverse consumption: load lines: %w", err)
	}

	// Split original vs prior compensating lines; prior reversals cap what remains reversible.
	var origLines []*ent.ConsumptionLine
	reversedSoFar := map[string]float64{} // key: recipeSKU|ingredientSKU → |Σ negative qty|
	lineKey := func(recipeSKU, ingredientSKU string) string { return recipeSKU + "|" + ingredientSKU }
	for _, l := range allLines {
		if l.Reason == reversalReason || l.Quantity < 0 {
			reversedSoFar[lineKey(l.RecipeSku, l.IngredientSku)] += math.Abs(l.Quantity)
			continue
		}
		origLines = append(origLines, l)
	}
	// The TRUE reversal ceiling for a key is the sum of EVERY original line sharing it, not any
	// one row's own Quantity — a SKU can appear on more than one ConsumptionLine for the same
	// order (a repeated sale line, or an Edit-Sale increase that recorded a SECOND consumption
	// row for a SKU already on the order). The loop below still caps each row's own reverseQty
	// against `remaining` (this key's shared budget), which correctly divides it across however
	// many rows share the key — but comparing against `l.Quantity` (this row's own total)
	// instead of the shared budget let one row's reversal inflate reversedSoFar past a SECOND
	// row's own quantity, silently skipping it — a systematic under-return of stock. Confirmed
	// by a worked example: line A qty=5, line B qty=2 (same key), reversing 2-of-7 total should
	// return exactly 2 (1.43 from A + 0.57 from B); the old per-row cap returned only 1.43,
	// dropping line B's share entirely.
	keyTotalQty := map[string]float64{}
	for _, l := range origLines {
		keyTotalQty[lineKey(l.RecipeSku, l.IngredientSku)] += l.Quantity
	}

	// Shortfall attribution: the per-entry shortfall lives only on the consumption header's
	// items JSON, keyed by (consumption event, ingredient sku) — NOT by any one
	// ConsumptionLine's own quantity. A single header entry's deduction can be split across
	// several ConsumptionLine rows (one per cost-layer/lot actually drawn from, plus a
	// standard-cost fallback line for whatever layers couldn't cover — see RecordConsumption's
	// lot-draw loop), so matching by exact (sku, quantity) against one line at a time silently
	// missed the shortfall whenever a sale's deduction spanned more than one line — previously
	// let a reversal restore more than was truly deducted. Group by (consumption_id, sku)
	// instead: sum every line's own quantity, subtract the header's shortfall ONCE for the
	// whole group, and apportion the result back across the group's lines by their own share —
	// correct regardless of how many layers/lines the deduction happened to split across, and
	// no longer order-dependent. See [[oversell-negative-stock-settlement]].
	groupKey := func(consumptionID uuid.UUID, sku string) string { return consumptionID.String() + "|" + sku }
	groupShortfall := map[string]float64{}
	for _, c := range originals {
		for _, it := range c.Items {
			if it.ShortfallQty > 0 {
				groupShortfall[groupKey(c.ID, it.SKU)] += it.ShortfallQty
			}
		}
	}
	groupTotalQty := map[string]float64{}
	for _, l := range origLines {
		if l.Theoretical {
			continue
		}
		groupTotalQty[groupKey(l.ConsumptionID, l.IngredientSku)] += l.Quantity
	}
	// deductedFor returns how much of line l's own quantity was actually taken out of stock
	// (net of its group's shortfall, apportioned by this line's share of the group total).
	deductedFor := func(l *ent.ConsumptionLine) float64 {
		if l.Theoretical {
			return 0
		}
		gKey := groupKey(l.ConsumptionID, l.IngredientSku)
		return apportionDeducted(l.Quantity, groupTotalQty[gKey], groupShortfall[gKey])
	}

	// Ratio per sale-line SKU (matched against the line's finished_item_sku, falling back
	// to recipe_sku). Empty Items = reverse everything (ratio 1 for all lines).
	ratioFor := func(l *ent.ConsumptionLine) float64 {
		if len(req.Items) == 0 {
			return 1
		}
		for _, it := range req.Items {
			if it.SKU != l.FinishedItemSku && it.SKU != l.RecipeSku {
				continue
			}
			if it.OfQuantity <= 0 || it.Quantity >= it.OfQuantity {
				return 1
			}
			return it.Quantity / it.OfQuantity
		}
		return 0
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: reverse consumption: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	reason := req.Reason
	if reason == "" {
		reason = "sale reversal"
	}
	now := time.Now()
	reversalID := uuid.New()
	var (
		results       []ReversedIngredient
		totalCost     float64
		compensations []entschema.ConsumptionItemJSON
	)

	for _, l := range origLines {
		ratio := ratioFor(l)
		if ratio <= 0 || l.Quantity <= 0 {
			continue
		}
		lineWarehouseID := uuid.Nil
		if l.WarehouseID != nil {
			lineWarehouseID = *l.WarehouseID
		}
		lineRecipeID := uuid.Nil
		if l.RecipeID != nil {
			lineRecipeID = *l.RecipeID
		}

		key := lineKey(l.RecipeSku, l.IngredientSku)
		reverseQty := capReverseQty(l.Quantity, ratio, keyTotalQty[key], reversedSoFar[key])
		if reverseQty <= 0 {
			continue
		}
		reversedSoFar[key] += reverseQty

		// Stock only ever moved for the deducted portion (theoretical lines and the
		// shortfall gap were recorded, not deducted). Prorate the deducted portion.
		deducted := deductedFor(l)
		stockReturn := round4(deducted * (reverseQty / l.Quantity))

		if stockReturn > 0 {
			itm, ierr := tx.Item.Query().
				Where(item.TenantID(tenantID), item.Sku(l.IngredientSku)).
				Only(ctx)
			if ierr != nil {
				s.log.Warn("consumption reversal: ingredient item missing — utilization compensated, no stock returned",
					zap.String("sku", l.IngredientSku), zap.Error(ierr))
				stockReturn = 0
			} else {
				bal, berr := tx.InventoryBalance.Query().
					Where(
						inventorybalance.TenantID(tenantID),
						inventorybalance.ItemID(itm.ID),
						inventorybalance.WarehouseID(lineWarehouseID),
					).
					First(ctx)
				switch {
				case berr == nil:
					beforeAvail := bal.Available
					afterAvail := bal.Available + stockReturn
					if _, uerr := tx.InventoryBalance.UpdateOne(bal).
						SetOnHand(bal.OnHand + stockReturn).
						SetAvailable(afterAvail).
						Save(ctx); uerr != nil {
						err = fmt.Errorf("stock: reverse consumption: restore balance sku=%s: %w", l.IngredientSku, uerr)
						return nil, err
					}
					// Sold-out menu items whose missing ingredient just came back must
					// resurface — same cascade the restock/adjustment paths run. Routed through
					// EmitStockChangeCascade (not a standalone cascadeIngredientRestocked call)
					// so this ALSO publishes stock.updated, keeping ordering's quantity-aware
					// catalog projection in sync — a gap found auditing this exact class of bug.
					s.EmitStockChangeCascade(ctx, tx, tenantID, itm.ID, lineWarehouseID, beforeAvail, afterAvail, "consumption_reversal")
				case ent.IsNotFound(berr):
					s.log.Warn("consumption reversal: no balance row — utilization compensated, no stock returned",
						zap.String("sku", l.IngredientSku))
					stockReturn = 0
				default:
					err = fmt.Errorf("stock: reverse consumption: query balance sku=%s: %w", l.IngredientSku, berr)
					return nil, err
				}
			}
		}

		// Negative compensating utilization line + daily-rollup decrement (same helpers the
		// forward path uses, so reports always net correctly).
		costReversed := round4(reverseQty * l.UnitCost)
		s.recordConsumptionLine(ctx, tx, tenantID, consumptionLineInput{
			consumptionID:    reversalID,
			orderID:          req.OrderID,
			orderNumber:      l.OrderNumber,
			customerName:     l.CustomerName,
			customerPhone:    l.CustomerPhone,
			servedByUserID:   l.ServedByUserID,
			servedByName:     l.ServedByName,
			warehouseID:      lineWarehouseID,
			outletID:         l.OutletID,
			recipeID:         lineRecipeID,
			recipeSKU:        l.RecipeSku,
			finishedItemSKU:  l.FinishedItemSku,
			ingredientItemID: l.IngredientItemID,
			ingredientSKU:    l.IngredientSku,
			quantity:         -reverseQty,
			unitCost:         l.UnitCost,
			theoretical:      l.Theoretical,
			reason:           reversalReason,
			consumedAt:       now,
		})

		totalCost += costReversed
		compensations = append(compensations, entschema.ConsumptionItemJSON{
			SKU:      l.IngredientSku,
			Quantity: -reverseQty,
		})
		results = append(results, ReversedIngredient{
			IngredientSKU:    l.IngredientSku,
			RecipeSKU:        l.RecipeSku,
			QuantityReversed: reverseQty,
			StockReturned:    stockReturn,
			CostReversed:     costReversed,
		})
	}

	if len(results) == 0 {
		_ = tx.Rollback()
		return nil, fmt.Errorf("stock: reverse consumption: nothing left to reverse for order %s (already reversed or no matching lines)", req.OrderID)
	}

	builder := tx.Consumption.Create().
		SetID(reversalID).
		SetTenantID(tenantID).
		SetOrderID(req.OrderID).
		SetItems(compensations).
		SetReason(reversalReason).
		SetStatus("processed").
		SetProcessedAt(now)
	if wh := originals[0].WarehouseID; wh != nil {
		builder.SetWarehouseID(*wh)
	}
	if req.IdempotencyKey != "" {
		builder.SetIdempotencyKey(req.IdempotencyKey)
	}
	if _, err = builder.Save(ctx); err != nil {
		return nil, fmt.Errorf("stock: reverse consumption: create compensating record: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, reversalID, "inventory", "stock.consumption_reversed", map[string]any{
		"order_id":            req.OrderID.String(),
		"reversed_at":         now.UTC().Format(time.RFC3339),
		"lines_count":         len(results),
		"total_cost_reversed": round4(totalCost),
		"reason":              reason,
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: reverse consumption: commit: %w", err)
	}

	s.log.Info("consumption reversed",
		zap.String("reversal_id", reversalID.String()),
		zap.String("order_id", req.OrderID.String()),
		zap.Int("lines", len(results)),
		zap.Float64("cost_reversed", round4(totalCost)),
	)

	return &ReverseConsumptionResponse{
		ID:                reversalID,
		OrderID:           req.OrderID,
		Status:            "processed",
		TotalCostReversed: round4(totalCost),
		Ingredients:       results,
	}, nil
}

// capReverseQty computes how much of ONE ConsumptionLine's quantity to reverse, given its own
// ratio (the sale-line proportion being reversed) and the shared key's total original quantity
// vs. what's already been reversed under that key (from prior calls, and — as this same
// function is called again for each later row sharing the key within a single
// ReverseConsumption call — from earlier rows processed in this same pass too). The ceiling is
// keyTotalQty (the SUM of every ConsumptionLine sharing this recipeSKU|ingredientSKU key), never
// this one line's own Quantity.
//
// Extracted as a pure function (no ent/DB types) specifically so this capping math is
// unit-testable without a database — this package has no ent test harness today. It fixes a
// real, confirmed-live bug: a SKU can appear on more than one ConsumptionLine for the same order
// (a repeated sale line, or an Edit-Sale increase that recorded a SECOND consumption row for a
// SKU already on the order). Capping against `lineQty` (that one row's own total) instead of the
// shared key budget let the FIRST such row's reversal inflate `reversedSoFarForKey` past the
// SECOND row's own quantity, silently dropping its share — a systematic under-return of stock
// (never an over-return). Worked example: line A qty=5, line B qty=2 (same key), reversing 2-of-7
// total should return exactly 2 (1.43 from A + 0.57 from B); the old per-row cap returned only
// 1.43, dropping line B's share entirely.
func capReverseQty(lineQty, ratio, keyTotalQty, reversedSoFarForKey float64) float64 {
	if ratio <= 0 || lineQty <= 0 {
		return 0
	}
	remaining := keyTotalQty - reversedSoFarForKey
	if remaining <= 0 {
		return 0
	}
	return math.Min(round4(lineQty*ratio), remaining)
}

// apportionDeducted returns how much of ONE ConsumptionLine's own quantity was actually taken
// out of stock, given the total quantity and total shortfall recorded across every line sharing
// its (consumption event, ingredient sku) group. The group's real deducted total
// (groupTotalQty - groupShortfall, floored at 0) is apportioned back across lines by each line's
// own share of groupTotalQty — correct no matter how many lines/lots the original deduction
// happened to split across, and independent of the order lines are processed in (unlike
// naively subtracting the full shortfall from just the first-matched line).
func apportionDeducted(lineQty, groupTotalQty, groupShortfall float64) float64 {
	if groupTotalQty <= 0 {
		return lineQty
	}
	groupDeducted := math.Max(0, groupTotalQty-groupShortfall)
	return round4(groupDeducted * (lineQty / groupTotalQty))
}
