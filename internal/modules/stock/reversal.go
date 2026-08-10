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

	// Shortfall attribution: the per-entry shortfall lives only on the consumption header
	// items JSON. Match header entries to lines by (sku, quantity), consuming each entry
	// once — identical duplicates (two of the same drink) are interchangeable, so first
	// match is safe.
	type headerEntry struct {
		entschema.ConsumptionItemJSON
		used bool
	}
	var headerEntries []*headerEntry
	for _, c := range originals {
		for i := range c.Items {
			headerEntries = append(headerEntries, &headerEntry{ConsumptionItemJSON: c.Items[i]})
		}
	}
	shortfallFor := func(l *ent.ConsumptionLine) float64 {
		for _, he := range headerEntries {
			if he.used || he.SKU != l.IngredientSku {
				continue
			}
			if math.Abs(he.Quantity-l.Quantity) > 0.0001 {
				continue
			}
			he.used = true
			return he.ShortfallQty
		}
		return 0
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

		// Cap by what earlier reversals left over so replays/overlapping partials can
		// never compensate more than was consumed.
		key := lineKey(l.RecipeSku, l.IngredientSku)
		remaining := l.Quantity - reversedSoFar[key]
		if remaining <= 0 {
			continue
		}
		reverseQty := math.Min(round4(l.Quantity*ratio), remaining)
		if reverseQty <= 0 {
			continue
		}
		reversedSoFar[key] += reverseQty

		// Stock only ever moved for the deducted portion (theoretical lines and the
		// shortfall gap were recorded, not deducted). Prorate the deducted portion.
		deducted := l.Quantity
		if l.Theoretical {
			deducted = 0
		} else if sf := shortfallFor(l); sf > 0 {
			deducted = math.Max(0, l.Quantity-sf)
		}
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
