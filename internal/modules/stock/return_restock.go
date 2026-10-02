package stock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Return restock outcomes reported back to the selling service.
const (
	ReturnRestocked        = "restocked"
	ReturnAlreadyRestocked = "already_restocked"
	ReturnNothingToRestock = "nothing_to_restock"
)

// ReturnRestockLine is one returned sale line. OfQuantity is the total quantity of that SKU
// sold on the order, so the reversal prorates the recorded consumption (returning 1 of 3
// plates returns a third of each ingredient). OfQuantity <= 0 means the whole SKU line.
type ReturnRestockLine struct {
	SKU        string  `json:"sku"`
	Quantity   float64 `json:"quantity"`
	OfQuantity float64 `json:"of_quantity,omitempty"`
}

// ReturnRestockRequest restocks the goods of one completed customer return.
type ReturnRestockRequest struct {
	ReturnID       uuid.UUID
	ReturnNumber   string
	OrderID        uuid.UUID
	OrderNumber    string
	CustomerName   string
	OutletID       uuid.UUID
	WarehouseID    uuid.UUID
	Lines          []ReturnRestockLine
	IdempotencyKey string
	// AllowDirectRestock lets SKUs with no recorded consumption on the order go straight back
	// into the outlet's warehouse. Callers pass false when the tenant is not entitled to
	// cross-service stock sync, since its sale never took the goods out either.
	AllowDirectRestock bool
}

// ReturnRestockedLine is where one returned SKU's stock went.
type ReturnRestockedLine struct {
	SKU           string    `json:"sku"`
	Quantity      float64   `json:"quantity"`
	WarehouseID   uuid.UUID `json:"warehouse_id"`
	WarehouseName string    `json:"warehouse_name,omitempty"`
	// OutletID is the outlet (branch) the warehouse serves: the "location" the selling service
	// shows. Nil for a shared/HQ warehouse, where the warehouse name is the best label.
	OutletID *uuid.UUID `json:"outlet_id,omitempty"`
	// Method is "reversal" (the sale's own recorded consumption was reversed, BOM-exact) or
	// "direct" (no consumption was recorded for the SKU, so the item itself was restocked).
	Method string `json:"method"`
}

// ReturnRestockResult summarizes a return restock for the selling service and the audit trail.
type ReturnRestockResult struct {
	Status string                `json:"status"`
	Lines  []ReturnRestockedLine `json:"lines"`
	// Skipped lists returned SKUs that put nothing back (non-stock items such as services, or
	// SKUs unknown to inventory), so the selling service can show why.
	Skipped []string `json:"skipped,omitempty"`
}

// RestockReturn puts a completed customer return's goods back into stock.
//
// Returned SKUs that the sale consumed here go through ReverseConsumption, which returns them to
// the exact warehouse the sale drew from, handles recipe/BOM items ingredient by ingredient, caps
// against what was sold (so an Edit-Sale reversal on the same order can never double-return) and
// writes the "Sell Return" rows the stock-history ledger shows. SKUs with no recorded consumption
// fall back to a direct restock into the outlet's warehouse, still ledgered and idempotent.
//
// Safe to replay: both legs are keyed on req.IdempotencyKey.
func (s *Service) RestockReturn(ctx context.Context, tenantID uuid.UUID, req ReturnRestockRequest) (*ReturnRestockResult, error) {
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("stock: restock return: idempotency key required")
	}
	lines := mergeReturnLines(req.Lines)
	if len(lines) == 0 {
		return &ReturnRestockResult{Status: ReturnNothingToRestock}, nil
	}

	consumed := map[string]bool{}
	if req.OrderID != uuid.Nil {
		var err error
		consumed, err = s.consumedSKUs(ctx, tenantID, req.OrderID, lines)
		if err != nil {
			return nil, err
		}
	}

	var reverseItems []ReverseConsumptionItem
	var direct []ReturnRestockLine
	for _, l := range lines {
		if consumed[l.SKU] {
			reverseItems = append(reverseItems, ReverseConsumptionItem{SKU: l.SKU, Quantity: l.Quantity, OfQuantity: l.OfQuantity})
		} else {
			direct = append(direct, l)
		}
	}

	result := &ReturnRestockResult{}
	replayed, fresh := 0, 0

	if len(reverseItems) > 0 {
		rev, err := s.ReverseConsumption(ctx, tenantID, ReverseConsumptionRequest{
			OrderID:        req.OrderID,
			Items:          reverseItems,
			Reason:         strings.TrimSpace("Customer return " + req.ReturnNumber),
			IdempotencyKey: req.IdempotencyKey,
		})
		switch {
		case errors.Is(err, ErrNothingToReverse):
			replayed++
		case err != nil:
			return nil, err
		case rev.AlreadyProcessed:
			replayed++
			result.Lines = append(result.Lines, s.reversedLinesFor(ctx, tenantID, rev.ID)...)
		default:
			fresh++
			result.Lines = append(result.Lines, reversedToLines(rev.Ingredients)...)
		}
	}

	if len(direct) > 0 {
		if !req.AllowDirectRestock {
			for _, l := range direct {
				result.Skipped = append(result.Skipped, l.SKU)
			}
		} else {
			restocked, skipped, already, err := s.directReturnRestock(ctx, tenantID, req, direct)
			if err != nil {
				return nil, err
			}
			result.Lines = append(result.Lines, restocked...)
			result.Skipped = append(result.Skipped, skipped...)
			if already {
				replayed++
			} else if len(restocked) > 0 {
				fresh++
			}
		}
	}

	s.nameWarehouses(ctx, tenantID, result.Lines)
	switch {
	case fresh > 0:
		result.Status = ReturnRestocked
	case replayed > 0:
		result.Status = ReturnAlreadyRestocked
	default:
		result.Status = ReturnNothingToRestock
	}
	return result, nil
}

// mergeReturnLines sums quantities per SKU (a SKU can sit on several sale lines) and drops
// blank or non-positive lines.
func mergeReturnLines(in []ReturnRestockLine) []ReturnRestockLine {
	idx := map[string]int{}
	out := make([]ReturnRestockLine, 0, len(in))
	for _, l := range in {
		sku := strings.TrimSpace(l.SKU)
		if sku == "" || l.Quantity <= 0 {
			continue
		}
		if i, ok := idx[sku]; ok {
			out[i].Quantity += l.Quantity
			if l.OfQuantity > out[i].OfQuantity {
				out[i].OfQuantity = l.OfQuantity
			}
			continue
		}
		idx[sku] = len(out)
		out = append(out, ReturnRestockLine{SKU: sku, Quantity: l.Quantity, OfQuantity: l.OfQuantity})
	}
	return out
}

// consumedSKUs reports which returned SKUs have original (non-reversal) consumption recorded on
// the order, matched the same way ReverseConsumption matches them (finished item or recipe SKU).
// One indexed query on (tenant_id, order_id), bounded by the order's own lines.
func (s *Service) consumedSKUs(ctx context.Context, tenantID, orderID uuid.UUID, lines []ReturnRestockLine) (map[string]bool, error) {
	skus := make([]string, 0, len(lines))
	for _, l := range lines {
		skus = append(skus, l.SKU)
	}
	rows, err := s.client.ConsumptionLine.Query().
		Where(
			entconsumptionline.TenantID(tenantID),
			entconsumptionline.OrderID(orderID),
			entconsumptionline.QuantityGT(0),
			entconsumptionline.Or(
				entconsumptionline.FinishedItemSkuIn(skus...),
				entconsumptionline.RecipeSkuIn(skus...),
			),
		).
		Select(entconsumptionline.FieldFinishedItemSku, entconsumptionline.FieldRecipeSku, entconsumptionline.FieldReason).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: restock return: load consumption: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.Reason == reversalReason {
			continue
		}
		out[r.FinishedItemSku] = true
		if r.RecipeSku != "" {
			out[r.RecipeSku] = true
		}
	}
	return out, nil
}

// directReturnRestock restocks SKUs the order never consumed here into the outlet's warehouse.
// It records a compensating Consumption (keyed "<key>-direct") plus negative "reversal" lines,
// so the stock-history ledger shows a Sell Return with the order number and a replay is a no-op.
// Lines skip the utilization rollup because nothing was consumed in the first place.
func (s *Service) directReturnRestock(ctx context.Context, tenantID uuid.UUID, req ReturnRestockRequest, lines []ReturnRestockLine) (restocked []ReturnRestockedLine, skipped []string, already bool, err error) {
	key := req.IdempotencyKey + "-direct"
	if prior, qerr := s.client.Consumption.Query().Where(entconsumption.IdempotencyKeyEQ(key)).First(ctx); qerr == nil {
		return s.reversedLinesFor(ctx, tenantID, prior.ID), nil, true, nil
	}

	whID, err := s.resolveWarehouseIDForOutlet(ctx, tenantID, req.WarehouseID, req.OutletID)
	if err != nil {
		return nil, nil, false, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("stock: direct return restock: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	outletID := s.resolveOutletID(ctx, tx, whID)
	consumptionID := uuid.New()
	now := time.Now()
	var compensations []entschema.ConsumptionItemJSON

	for _, l := range lines {
		itm, ierr := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(l.SKU)).
			WithUnits().
			First(ctx)
		if ierr != nil || holdsNoStock(itm) || itm.Type == item.TypeRECIPE {
			// Unknown SKU, a service/voucher, or a recipe whose sale never exploded its BOM:
			// there is no stock row this return can honestly add to.
			skipped = append(skipped, l.SKU)
			continue
		}

		bal, berr := tx.InventoryBalance.Query().
			Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(itm.ID), inventorybalance.WarehouseID(whID)).
			First(ctx)
		before := 0.0
		switch {
		case berr == nil:
			before = bal.Available
			if _, err = tx.InventoryBalance.UpdateOne(bal).
				SetOnHand(bal.OnHand + l.Quantity).
				SetAvailable(bal.Available + l.Quantity).
				Save(ctx); err != nil {
				return nil, nil, false, fmt.Errorf("stock: direct return restock sku=%s: %w", l.SKU, err)
			}
		case ent.IsNotFound(berr):
			if _, err = tx.InventoryBalance.Create().
				SetTenantID(tenantID).SetItemID(itm.ID).SetWarehouseID(whID).
				SetOnHand(l.Quantity).SetAvailable(l.Quantity).SetReserved(0).
				Save(ctx); err != nil {
				return nil, nil, false, fmt.Errorf("stock: direct return restock create balance sku=%s: %w", l.SKU, err)
			}
		default:
			err = fmt.Errorf("stock: direct return restock balance sku=%s: %w", l.SKU, berr)
			return nil, nil, false, err
		}
		s.EmitStockChangeCascade(ctx, tx, tenantID, itm.ID, whID, before, before+l.Quantity, "customer_return")

		s.recordConsumptionLine(ctx, tx, tenantID, consumptionLineInput{
			consumptionID:    consumptionID,
			orderID:          req.OrderID,
			orderNumber:      req.OrderNumber,
			customerName:     req.CustomerName,
			warehouseID:      whID,
			outletID:         outletID,
			recipeSKU:        l.SKU,
			finishedItemSKU:  l.SKU,
			ingredientItemID: itm.ID,
			ingredientSKU:    l.SKU,
			quantity:         -l.Quantity,
			unitCost:         itemCostPrice(itm),
			reason:           reversalReason,
			consumedAt:       now,
			skipRollup:       true,
		})
		compensations = append(compensations, entschema.ConsumptionItemJSON{SKU: l.SKU, Quantity: -l.Quantity})
		restocked = append(restocked, ReturnRestockedLine{SKU: l.SKU, Quantity: l.Quantity, WarehouseID: whID, Method: "direct"})
	}

	if len(compensations) == 0 {
		_ = tx.Rollback()
		return nil, skipped, false, nil
	}

	builder := tx.Consumption.Create().
		SetID(consumptionID).
		SetTenantID(tenantID).
		SetItems(compensations).
		SetReason(reversalReason).
		SetStatus("processed").
		SetProcessedAt(now).
		SetWarehouseID(whID).
		SetIdempotencyKey(key)
	if req.OrderID != uuid.Nil {
		builder.SetOrderID(req.OrderID)
	} else {
		builder.SetOrderID(req.ReturnID)
	}
	if _, err = builder.Save(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("stock: direct return restock: record: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, false, fmt.Errorf("stock: direct return restock: commit: %w", err)
	}

	s.log.Info("return restocked directly (no recorded consumption)",
		zap.String("return_number", req.ReturnNumber), zap.String("warehouse_id", whID.String()),
		zap.Int("lines", len(restocked)))
	return restocked, skipped, false, nil
}

// reversedLinesFor rebuilds a prior restock's per-SKU outcome from its compensating lines, so a
// replayed event still reports the real location instead of an empty result.
func (s *Service) reversedLinesFor(ctx context.Context, tenantID, consumptionID uuid.UUID) []ReturnRestockedLine {
	rows, err := s.client.ConsumptionLine.Query().
		Where(entconsumptionline.TenantID(tenantID), entconsumptionline.ConsumptionID(consumptionID)).
		Select(entconsumptionline.FieldIngredientSku, entconsumptionline.FieldQuantity, entconsumptionline.FieldWarehouseID, entconsumptionline.FieldTheoretical).
		All(ctx)
	if err != nil {
		return nil
	}
	ingredients := make([]ReversedIngredient, 0, len(rows))
	for _, r := range rows {
		wh := uuid.Nil
		if r.WarehouseID != nil {
			wh = *r.WarehouseID
		}
		qty := -r.Quantity
		returned := qty
		if r.Theoretical {
			returned = 0
		}
		ingredients = append(ingredients, ReversedIngredient{IngredientSKU: r.IngredientSku, QuantityReversed: qty, StockReturned: returned, WarehouseID: wh})
	}
	return reversedToLines(ingredients)
}

// reversedToLines folds ingredient-level reversal results into one row per (SKU, warehouse),
// keeping only stock that actually went back on hand.
func reversedToLines(ings []ReversedIngredient) []ReturnRestockedLine {
	type k struct {
		sku string
		wh  uuid.UUID
	}
	idx := map[k]int{}
	var out []ReturnRestockedLine
	for _, ing := range ings {
		if ing.StockReturned <= 0 {
			continue
		}
		key := k{ing.IngredientSKU, ing.WarehouseID}
		if i, ok := idx[key]; ok {
			out[i].Quantity = round4(out[i].Quantity + ing.StockReturned)
			continue
		}
		idx[key] = len(out)
		out = append(out, ReturnRestockedLine{SKU: ing.IngredientSKU, Quantity: round4(ing.StockReturned), WarehouseID: ing.WarehouseID, Method: "reversal"})
	}
	return out
}

// PublishReturnRestockOutcome writes inventory.return.restocked to the outbox so the selling
// service can show where (or whether) a return's goods went back. Status is one of the Return*
// constants, or "failed" with errMsg when the restock gave up after its last retry.
func (s *Service) PublishReturnRestockOutcome(ctx context.Context, tenantID, returnID uuid.UUID, source string, result *ReturnRestockResult, status, errMsg string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("stock: return outcome: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	payload := map[string]any{
		"tenant_id":    tenantID.String(),
		"return_id":    returnID.String(),
		"source":       source,
		"status":       status,
		"processed_at": time.Now().UTC().Format(time.RFC3339),
	}
	if result != nil {
		payload["lines"] = result.Lines
		if len(result.Skipped) > 0 {
			payload["skipped"] = result.Skipped
		}
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	s.writeOutboxEvent(ctx, tx, tenantID, returnID, "inventory", "return.restocked", payload)
	return tx.Commit()
}

// customerReturnConsumptions reports which compensating consumptions among these ledger lines
// came from a customer return or exchange (idempotency key "pos-return-..." or
// "pos-exchange-..."), as opposed to a sale reversal or edit. One batched query.
func (s *Service) customerReturnConsumptions(ctx context.Context, lines []*ent.ConsumptionLine) map[uuid.UUID]bool {
	ids := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, l := range lines {
		if (l.Reason == reversalReason || l.Quantity < 0) && !seen[l.ConsumptionID] {
			seen[l.ConsumptionID] = true
			ids = append(ids, l.ConsumptionID)
		}
	}
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out
	}
	rows, err := s.client.Consumption.Query().
		Where(entconsumption.IDIn(ids...), entconsumption.IdempotencyKeyNotNil()).
		Select(entconsumption.FieldID, entconsumption.FieldIdempotencyKey).
		All(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if r.IdempotencyKey != nil && (strings.HasPrefix(*r.IdempotencyKey, "pos-return-") || strings.HasPrefix(*r.IdempotencyKey, "pos-exchange-")) {
			out[r.ID] = true
		}
	}
	return out
}

// nameWarehouses fills WarehouseName and OutletID with one batched lookup.
func (s *Service) nameWarehouses(ctx context.Context, tenantID uuid.UUID, lines []ReturnRestockedLine) {
	ids := make([]uuid.UUID, 0, len(lines))
	seen := map[uuid.UUID]bool{}
	for _, l := range lines {
		if l.WarehouseID != uuid.Nil && !seen[l.WarehouseID] {
			seen[l.WarehouseID] = true
			ids = append(ids, l.WarehouseID)
		}
	}
	if len(ids) == 0 {
		return
	}
	whs, err := s.client.Warehouse.Query().
		Where(warehouse.TenantID(tenantID), warehouse.IDIn(ids...)).
		Select(warehouse.FieldID, warehouse.FieldName, warehouse.FieldOutletID).
		All(ctx)
	if err != nil {
		return
	}
	byID := make(map[uuid.UUID]*ent.Warehouse, len(whs))
	for _, w := range whs {
		byID[w.ID] = w
	}
	for i := range lines {
		if w := byID[lines[i].WarehouseID]; w != nil {
			lines[i].WarehouseName = w.Name
			lines[i].OutletID = w.OutletID
		}
	}
}
