package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/ent/reservation"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// ReservationRequest matches the ordering-backend client DTO.
type ReservationRequest struct {
	TenantID       uuid.UUID         `json:"tenant_id"`
	OrderID        uuid.UUID         `json:"order_id"`
	WarehouseID    uuid.UUID         `json:"warehouse_id,omitempty"`
	Items          []ReservationItem `json:"items"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// ReservationItem represents a single item to reserve (fractional-capable).
type ReservationItem struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// ReservationResponse matches the ordering-backend client DTO.
type ReservationResponse struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    uuid.UUID      `json:"tenant_id"`
	OrderID     uuid.UUID      `json:"order_id"`
	Status      string         `json:"status"`
	Items       []ReservedItem `json:"items"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
	ConfirmedAt *time.Time     `json:"confirmed_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// ReservedItem matches the ordering-backend client DTO.
type ReservedItem struct {
	SKU             string  `json:"sku"`
	RequestedQty    float64 `json:"requested_qty"`
	ReservedQty     float64 `json:"reserved_qty"`
	AvailableQty    float64 `json:"available_qty"`
	IsFullyReserved bool    `json:"is_fully_reserved"`
}

// ConsumptionRequest matches the ordering-backend client DTO.
type ConsumptionRequest struct {
	TenantID       uuid.UUID         `json:"tenant_id"`
	OrderID        uuid.UUID         `json:"order_id"`
	WarehouseID    uuid.UUID         `json:"warehouse_id,omitempty"`
	Items          []ConsumptionItem `json:"items"`
	Reason         string            `json:"reason,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// ConsumptionItem represents an item to consume.
type ConsumptionItem struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// ConsumptionResponse matches the ordering-backend client DTO.
type ConsumptionResponse struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	OrderID     uuid.UUID `json:"order_id"`
	Status      string    `json:"status"`
	ProcessedAt time.Time `json:"processed_at"`
}

// Service handles stock reservation and consumption business logic.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new stock service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("stock.service"),
	}
}

// AdjustStockRequest represents a stock adjustment request.
type AdjustStockRequest struct {
	SKU         string     `json:"sku"`
	Adjustment  float64    `json:"adjustment"`
	Reason      string     `json:"reason"`
	Reference   string     `json:"reference,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	AdjustedBy  uuid.UUID  `json:"adjusted_by"`
	WarehouseID uuid.UUID  `json:"warehouse_id,omitempty"`
	UnitID      *uuid.UUID `json:"unit_id,omitempty"` // optional; when set, records the balance's unit of measure
}

// AdjustStockResponse represents the result of a stock adjustment.
type AdjustStockResponse struct {
	ID           uuid.UUID `json:"id"`
	SKU          string    `json:"sku"`
	OnHand       float64   `json:"on_hand"`
	Available    float64   `json:"available"`
	Reserved     float64   `json:"reserved"`
	Reason       string    `json:"reason"`
	QtyBefore    float64   `json:"quantity_before"`
	QtyChange    float64   `json:"quantity_change"`
	QtyAfter     float64   `json:"quantity_after"`
	AdjustedAt   time.Time `json:"adjusted_at"`
}

// StockAdjustmentDTO represents a stock adjustment for listing.
type StockAdjustmentDTO struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	ItemID         uuid.UUID `json:"item_id"`
	ItemName       string    `json:"item_name,omitempty"`
	WarehouseID    uuid.UUID `json:"warehouse_id"`
	WarehouseName  string    `json:"warehouse_name,omitempty"`
	QuantityBefore float64   `json:"quantity_before"`
	QuantityChange float64   `json:"quantity_change"`
	QuantityAfter  float64   `json:"quantity_after"`
	Reason         string    `json:"reason"`
	Reference      string    `json:"reference,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	AdjustedBy     uuid.UUID `json:"adjusted_by"`
	AdjustedAt     time.Time `json:"adjusted_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// ListAdjustmentsRequest contains filters for listing stock adjustments.
type ListAdjustmentsRequest struct {
	ItemID      uuid.UUID `json:"item_id,omitempty"`
	WarehouseID uuid.UUID `json:"warehouse_id,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	DateFrom    time.Time `json:"date_from,omitempty"`
	DateTo      time.Time `json:"date_to,omitempty"`
}

// AdjustStock adjusts stock levels for an item, creates an audit trail, and publishes events.
func (s *Service) AdjustStock(ctx context.Context, tenantID uuid.UUID, req AdjustStockRequest) (*AdjustStockResponse, error) {
	whID, err := s.resolveWarehouseID(ctx, tenantID, req.WarehouseID)
	if err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	itm, err := tx.Item.Query().
		Where(item.TenantID(tenantID), item.Sku(req.SKU)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: item not found: sku=%s: %w", req.SKU, err)
	}

	bal, err := tx.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itm.ID),
			inventorybalance.WarehouseID(whID),
		).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("stock: query balance: %w", err)
		}
		// No balance row exists yet for this item in this warehouse. Rather than failing the
		// adjustment (the common case for Initial Stock Count / Found on a fresh item),
		// initialize a zero balance and apply the adjustment against it.
		bal, err = tx.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itm.ID).
			SetWarehouseID(whID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("stock: init balance for sku=%s: %w", req.SKU, err)
		}
	}

	qtyBefore := float64(bal.OnHand)
	qtyChange := float64(req.Adjustment)

	newOnHand := bal.OnHand + req.Adjustment
	if newOnHand < 0 {
		newOnHand = 0
	}
	newAvailable := bal.Available + req.Adjustment
	if newAvailable < 0 {
		newAvailable = 0
	}

	qtyAfter := float64(newOnHand)

	balUpdate := tx.InventoryBalance.UpdateOne(bal).
		SetOnHand(newOnHand).
		SetAvailable(newAvailable)
	// Record the unit of measure when the caller specifies one (defaults to the
	// existing balance UoM / item base unit when omitted).
	if req.UnitID != nil {
		if u, uErr := tx.Unit.Get(ctx, *req.UnitID); uErr == nil && u.Name != "" {
			balUpdate = balUpdate.SetUnitOfMeasure(u.Name)
		}
	}
	updatedBal, err := balUpdate.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: update balance for sku=%s: %w", req.SKU, err)
	}

	// Validate reason for the enum
	adjReason := stockadjustment.Reason(req.Reason)
	if err := stockadjustment.ReasonValidator(adjReason); err != nil {
		adjReason = stockadjustment.ReasonOther
	}

	// Create StockAdjustment audit record
	now := time.Now()
	adjBuilder := tx.StockAdjustment.Create().
		SetTenantID(tenantID).
		SetItemID(itm.ID).
		SetWarehouseID(whID).
		SetQuantityBefore(qtyBefore).
		SetQuantityChange(qtyChange).
		SetQuantityAfter(qtyAfter).
		SetReason(adjReason).
		SetAdjustedAt(now)

	if req.AdjustedBy != uuid.Nil {
		adjBuilder.SetAdjustedBy(req.AdjustedBy)
	} else {
		adjBuilder.SetAdjustedBy(uuid.Nil)
	}
	if req.Reference != "" {
		adjBuilder.SetReference(req.Reference)
	}
	if req.Notes != "" {
		adjBuilder.SetNotes(req.Notes)
	}

	adj, err := adjBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: create adjustment record: %w", err)
	}

	// Publish stock updated event
	s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.updated", map[string]any{
		"tenant_id":       tenantID.String(),
		"item_id":         itm.ID.String(),
		"sku":             itm.Sku,
		"warehouse_id":    whID.String(),
		"adjustment_id":   adj.ID.String(),
		"quantity_before": qtyBefore,
		"quantity_change": qtyChange,
		"quantity_after":  qtyAfter,
		"reason":          req.Reason,
		"on_hand":         newOnHand,
		"available":       newAvailable,
	})

	// Check for low stock and publish event
	s.checkAndPublishLowStock(ctx, tx, tenantID, itm, updatedBal, whID)

	// If a positive correction lifted this ingredient back above zero, re-enable any
	// recipes its depletion had gated. The goods-receipt path already cascades a
	// restock (line ~1081); a corrective upward adjustment must do the same, or
	// recipes disabled by the stock-out cascade would stay hidden until a receipt.
	if qtyBefore <= 0 && qtyAfter > 0 {
		s.cascadeIngredientRestocked(ctx, tx, tenantID, itm.ID, whID)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit adjustment: %w", err)
	}

	s.log.Info("stock adjusted",
		zap.String("sku", req.SKU),
		zap.Float64("adjustment", req.Adjustment),
		zap.String("reason", req.Reason),
		zap.Float64("new_on_hand", newOnHand),
		zap.String("adjustment_id", adj.ID.String()),
	)

	return &AdjustStockResponse{
		ID:         adj.ID,
		SKU:        req.SKU,
		OnHand:     newOnHand,
		Available:  newAvailable,
		Reserved:   bal.Reserved,
		Reason:     req.Reason,
		QtyBefore:  qtyBefore,
		QtyChange:  qtyChange,
		QtyAfter:   qtyAfter,
		AdjustedAt: now,
	}, nil
}

// ListAdjustments returns stock adjustments filtered by the given criteria.
func (s *Service) ListAdjustments(ctx context.Context, tenantID uuid.UUID, req ListAdjustmentsRequest) ([]StockAdjustmentDTO, error) {
	q := s.client.StockAdjustment.Query().
		Where(stockadjustment.TenantID(tenantID))

	if req.ItemID != uuid.Nil {
		q = q.Where(stockadjustment.ItemID(req.ItemID))
	}
	if req.WarehouseID != uuid.Nil {
		q = q.Where(stockadjustment.WarehouseID(req.WarehouseID))
	}
	if req.Reason != "" {
		reason := stockadjustment.Reason(req.Reason)
		if stockadjustment.ReasonValidator(reason) == nil {
			q = q.Where(stockadjustment.ReasonEQ(reason))
		}
	}
	if !req.DateFrom.IsZero() {
		q = q.Where(stockadjustment.AdjustedAtGTE(req.DateFrom))
	}
	if !req.DateTo.IsZero() {
		q = q.Where(stockadjustment.AdjustedAtLTE(req.DateTo))
	}

	adjustments, err := q.
		Order(ent.Desc(stockadjustment.FieldAdjustedAt)).
		Limit(200).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: list adjustments: %w", err)
	}

	// Collect unique item and warehouse IDs for batch lookup.
	itemIDSet := make(map[uuid.UUID]struct{})
	whIDSet := make(map[uuid.UUID]struct{})
	for _, a := range adjustments {
		itemIDSet[a.ItemID] = struct{}{}
		whIDSet[a.WarehouseID] = struct{}{}
	}

	itemIDs := make([]uuid.UUID, 0, len(itemIDSet))
	for id := range itemIDSet {
		itemIDs = append(itemIDs, id)
	}
	whIDs := make([]uuid.UUID, 0, len(whIDSet))
	for id := range whIDSet {
		whIDs = append(whIDs, id)
	}

	// Batch-fetch item names.
	itemNames := make(map[uuid.UUID]string)
	if len(itemIDs) > 0 {
		items, itemErr := s.client.Item.Query().
			Where(item.IDIn(itemIDs...)).
			Select(item.FieldID, item.FieldName).
			All(ctx)
		if itemErr == nil {
			for _, itm := range items {
				itemNames[itm.ID] = itm.Name
			}
		}
	}

	// Batch-fetch warehouse names.
	warehouseNames := make(map[uuid.UUID]string)
	if len(whIDs) > 0 {
		warehouses, whErr := s.client.Warehouse.Query().
			Where(warehouse.IDIn(whIDs...)).
			Select(warehouse.FieldID, warehouse.FieldName).
			All(ctx)
		if whErr == nil {
			for _, wh := range warehouses {
				warehouseNames[wh.ID] = wh.Name
			}
		}
	}

	result := make([]StockAdjustmentDTO, len(adjustments))
	for i, a := range adjustments {
		result[i] = StockAdjustmentDTO{
			ID:             a.ID,
			TenantID:       a.TenantID,
			ItemID:         a.ItemID,
			ItemName:       itemNames[a.ItemID],
			WarehouseID:    a.WarehouseID,
			WarehouseName:  warehouseNames[a.WarehouseID],
			QuantityBefore: a.QuantityBefore,
			QuantityChange: a.QuantityChange,
			QuantityAfter:  a.QuantityAfter,
			Reason:         string(a.Reason),
			Reference:      a.Reference,
			Notes:          a.Notes,
			AdjustedBy:     a.AdjustedBy,
			AdjustedAt:     a.AdjustedAt,
			CreatedAt:      a.CreatedAt,
		}
	}
	return result, nil
}

// checkAndPublishLowStock checks if stock is at or below reorder level and publishes an event.
// Also publishes a stock-out event when available reaches zero.
func (s *Service) checkAndPublishLowStock(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, itm *ent.Item, bal *ent.InventoryBalance, warehouseID uuid.UUID) {
	outletID := s.outletIDForWarehouse(ctx, tx, warehouseID)
	if bal.Available <= 0 {
		s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.out", map[string]any{
			"tenant_id":    tenantID.String(),
			"item_id":      itm.ID.String(),
			"sku":          itm.Sku,
			"name":         itm.Name,
			"available":    bal.Available,
			"warehouse_id": warehouseID.String(),
			"outlet_id":    outletID,
			"notification": map[string]any{
				"target": "staff",
			},
		})
		s.log.Warn("stock-out alert published",
			zap.String("sku", itm.Sku),
			zap.Float64("available", bal.Available),
		)
		// Cascade: mark recipe items as unavailable when an ingredient runs out.
		s.cascadeIngredientStockOut(ctx, tx, tenantID, itm.ID, warehouseID)
	} else if bal.Available <= float64(bal.ReorderLevel) {
		s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.low", map[string]any{
			"tenant_id":     tenantID.String(),
			"item_id":       itm.ID.String(),
			"sku":           itm.Sku,
			"name":          itm.Name,
			"available":     bal.Available,
			"reorder_level": bal.ReorderLevel,
			"warehouse_id":  warehouseID.String(),
			"notification": map[string]any{
				"target": "staff",
			},
		})
		s.log.Info("low stock alert published",
			zap.String("sku", itm.Sku),
			zap.Float64("available", bal.Available),
			zap.Int("reorder_level", bal.ReorderLevel),
		)
	}
}

// resolveWarehouseID returns the provided warehouse ID or the tenant's default warehouse.
func (s *Service) resolveWarehouseID(ctx context.Context, tenantID, warehouseID uuid.UUID) (uuid.UUID, error) {
	if warehouseID != uuid.Nil {
		return warehouseID, nil
	}
	wh, err := s.client.Warehouse.Query().
		Where(
			warehouse.TenantID(tenantID),
			warehouse.IsDefault(true),
			warehouse.IsActive(true),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// Include the resolved tenant so callers can see when an S2S request
			// resolved a wrong/empty tenant (e.g. consumption from a NATS-driven path).
			return uuid.Nil, fmt.Errorf("stock: no default warehouse for tenant %s", tenantID)
		}
		return uuid.Nil, fmt.Errorf("stock: query default warehouse: %w", err)
	}
	return wh.ID, nil
}

// explodedIngredient holds a single resolved ingredient SKU + quantity.
type explodedIngredient struct {
	SKU      string
	Quantity float64
}

// explodeBOM resolves a menu-item SKU to its raw ingredients using the recipe/BOM table.
// If the SKU has no recipe or the recipe has no ingredients, returns the original SKU × qty.
// portionsRequested is the number of portions of the menu item to produce.
func (s *Service) explodeBOM(ctx context.Context, tenantID uuid.UUID, sku string, portionsRequested float64) ([]explodedIngredient, bool) {
	r, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(sku), recipe.IsActive(true)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.Order(ent.Asc(recipeingredient.FieldDisplayOrder))
		}).
		Only(ctx)
	if err != nil || len(r.Edges.Ingredients) == 0 {
		return nil, false
	}

	outputQty := r.OutputQty
	if outputQty <= 0 {
		outputQty = 1
	}

	ingredients := make([]explodedIngredient, 0, len(r.Edges.Ingredients))
	for _, ing := range r.Edges.Ingredients {
		// Scale ingredient by (portions / outputQty). Keep the result fractional —
		// a recipe needing 0.5 L per portion must reserve/consume 0.5 L, not 1.
		qty := (ing.Quantity / outputQty) * portionsRequested
		if qty < 0 {
			qty = 0
		}
		ingredients = append(ingredients, explodedIngredient{SKU: ing.ItemSku, Quantity: qty})
	}
	return ingredients, true
}

// CreateReservation reserves stock for an order within a transaction.
// If a requested SKU has a recipe, the BOM is exploded and raw ingredients are reserved.
func (s *Service) CreateReservation(ctx context.Context, tenantID uuid.UUID, req ReservationRequest) (*ReservationResponse, error) {
	whID, err := s.resolveWarehouseID(ctx, tenantID, req.WarehouseID)
	if err != nil {
		return nil, err
	}

	// Check idempotency
	if req.IdempotencyKey != "" {
		existing, err := s.client.Reservation.Query().
			Where(reservation.IdempotencyKey(req.IdempotencyKey)).
			First(ctx)
		if err == nil {
			return s.mapReservation(existing), nil
		}
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	reservedItems := make([]entschema.ReservedItemJSON, 0, len(req.Items))

	for _, ri := range req.Items {
		// Expand recipe/BOM into raw ingredients before reserving.
		// If the SKU has no recipe, falls back to direct reservation.
		ingredientsToReserve, isBOM := s.explodeBOM(ctx, tenantID, ri.SKU, ri.Quantity)
		if !isBOM {
			ingredientsToReserve = []explodedIngredient{{SKU: ri.SKU, Quantity: ri.Quantity}}
		}

		totalReservedQty := 0.0
		fullyReserved := true

		for _, ing := range ingredientsToReserve {
			itm, err := tx.Item.Query().
				Where(item.TenantID(tenantID), item.Sku(ing.SKU), item.IsActive(true)).
				Only(ctx)
			if err != nil {
				s.log.Warn("ingredient item not found during BOM explosion",
					zap.String("sku", ing.SKU), zap.Error(err))
				fullyReserved = false
				continue
			}

			bal, err := tx.InventoryBalance.Query().
				Where(
					inventorybalance.TenantID(tenantID),
					inventorybalance.ItemID(itm.ID),
					inventorybalance.WarehouseID(whID),
				).
				First(ctx)

			var availableQty float64
			if err != nil {
				if ent.IsNotFound(err) {
					availableQty = 0
				} else {
					return nil, fmt.Errorf("stock: query balance: sku=%s: %w", ing.SKU, err)
				}
			} else {
				availableQty = bal.Available
			}

			reserveQty := ing.Quantity
			if reserveQty > availableQty {
				reserveQty = availableQty
				fullyReserved = false
			}

			if bal != nil && reserveQty > 0 {
				_, err = tx.InventoryBalance.UpdateOne(bal).
					SetAvailable(bal.Available - reserveQty).
					SetReserved(bal.Reserved + reserveQty).
					Save(ctx)
				if err != nil {
					return nil, fmt.Errorf("stock: update balance for sku=%s: %w", ing.SKU, err)
				}
			}

			// For BOM items, only count ingredient reservations proportionally.
			if isBOM {
				totalReservedQty = ri.Quantity // treat as reserved at menu-item level
			} else {
				totalReservedQty = reserveQty
			}

			// Record each ingredient reservation for BOM items (for audit/release).
			if isBOM {
				reservedItems = append(reservedItems, entschema.ReservedItemJSON{
					SKU:             ing.SKU,
					RequestedQty:    ing.Quantity,
					ReservedQty:     reserveQty,
					AvailableQty:    availableQty,
					IsFullyReserved: reserveQty >= ing.Quantity,
				})
			}
		}

		if !isBOM {
			// Direct item reservation — record with original SKU.
			reservedItems = append(reservedItems, entschema.ReservedItemJSON{
				SKU:             ri.SKU,
				RequestedQty:    ri.Quantity,
				ReservedQty:     totalReservedQty,
				AvailableQty:    totalReservedQty,
				IsFullyReserved: fullyReserved,
			})
		} else if fullyReserved {
			// Add a summary entry for the composite (menu-item) SKU.
			reservedItems = append(reservedItems, entschema.ReservedItemJSON{
				SKU:             ri.SKU,
				RequestedQty:    ri.Quantity,
				ReservedQty:     ri.Quantity,
				AvailableQty:    ri.Quantity,
				IsFullyReserved: true,
			})
		}
	}

	builder := tx.Reservation.Create().
		SetTenantID(tenantID).
		SetOrderID(req.OrderID).
		SetWarehouseID(whID).
		SetStatus("pending").
		SetItems(reservedItems)

	if req.ExpiresAt != nil {
		builder.SetExpiresAt(*req.ExpiresAt)
	}
	if req.IdempotencyKey != "" {
		builder.SetIdempotencyKey(req.IdempotencyKey)
	}

	resv, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: create reservation: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, resv.ID, "inventory", "reservation.confirmed", map[string]any{
		"order_id":    req.OrderID.String(),
		"warehouse_id": whID.String(),
		"items":       reservedItems,
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit reservation: %w", err)
	}

	s.log.Info("reservation created",
		zap.String("reservation_id", resv.ID.String()),
		zap.String("order_id", req.OrderID.String()),
		zap.Int("items", len(reservedItems)),
	)

	return s.mapReservation(resv), nil
}

// GetReservation returns a reservation by ID.
func (s *Service) GetReservation(ctx context.Context, tenantID, reservationID uuid.UUID) (*ReservationResponse, error) {
	resv, err := s.client.Reservation.Query().
		Where(
			reservation.ID(reservationID),
			reservation.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("stock: reservation not found")
		}
		return nil, fmt.Errorf("stock: query reservation: %w", err)
	}
	return s.mapReservation(resv), nil
}

// GetReservationsByOrderID returns reservations for an order.
func (s *Service) GetReservationsByOrderID(ctx context.Context, tenantID, orderID uuid.UUID) ([]ReservationResponse, error) {
	reservations, err := s.client.Reservation.Query().
		Where(
			reservation.TenantID(tenantID),
			reservation.OrderID(orderID),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: query reservations by order: %w", err)
	}

	result := make([]ReservationResponse, len(reservations))
	for i, r := range reservations {
		result[i] = *s.mapReservation(r)
	}
	return result, nil
}

// ReleaseReservation releases a stock reservation, restoring available quantities.
func (s *Service) ReleaseReservation(ctx context.Context, tenantID, reservationID uuid.UUID, reason string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	resv, err := tx.Reservation.Query().
		Where(
			reservation.ID(reservationID),
			reservation.TenantID(tenantID),
			reservation.StatusIn("pending", "confirmed"),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("stock: reservation not found or already released")
		}
		return fmt.Errorf("stock: query reservation: %w", err)
	}

	whID := uuid.Nil
	if resv.WarehouseID != nil {
		whID = *resv.WarehouseID
	}

	for _, ri := range resv.Items {
		if ri.ReservedQty <= 0 {
			continue
		}

		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(ri.SKU)).
			Only(ctx)
		if err != nil {
			continue
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)
		if err != nil {
			continue
		}

		_, err = tx.InventoryBalance.UpdateOne(bal).
			SetAvailable(bal.Available + ri.ReservedQty).
			SetReserved(max(0, bal.Reserved-ri.ReservedQty)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("stock: update balance for sku=%s: %w", ri.SKU, err)
		}
	}

	_, err = tx.Reservation.UpdateOne(resv).
		SetStatus("released").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("stock: update reservation status: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, reservationID, "inventory", "reservation.released", map[string]any{
		"order_id": resv.OrderID.String(),
		"reason":   reason,
	})

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("stock: commit release: %w", err)
	}

	s.log.Info("reservation released",
		zap.String("reservation_id", reservationID.String()),
		zap.String("reason", reason),
	)
	return nil
}

// ConsumeReservation converts a reservation to actual consumption, deducting on-hand stock.
func (s *Service) ConsumeReservation(ctx context.Context, tenantID, reservationID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	resv, err := tx.Reservation.Query().
		Where(
			reservation.ID(reservationID),
			reservation.TenantID(tenantID),
			reservation.StatusIn("pending", "confirmed"),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("stock: reservation not found or already consumed")
		}
		return fmt.Errorf("stock: query reservation: %w", err)
	}

	whID := uuid.Nil
	if resv.WarehouseID != nil {
		whID = *resv.WarehouseID
	}

	for _, ri := range resv.Items {
		if ri.ReservedQty <= 0 {
			continue
		}

		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(ri.SKU)).
			Only(ctx)
		if err != nil {
			continue
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)
		if err != nil {
			continue
		}

		updatedBal, updateErr := tx.InventoryBalance.UpdateOne(bal).
			SetOnHand(max(0, bal.OnHand-ri.ReservedQty)).
			SetReserved(max(0, bal.Reserved-ri.ReservedQty)).
			Save(ctx)
		if updateErr != nil {
			err = updateErr
			return fmt.Errorf("stock: update balance for sku=%s: %w", ri.SKU, err)
		}

		// Check for low stock after consumption
		s.checkAndPublishLowStock(ctx, tx, tenantID, itm, updatedBal, whID)
	}

	now := time.Now()
	_, err = tx.Reservation.UpdateOne(resv).
		SetStatus("consumed").
		SetConfirmedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("stock: update reservation status: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, reservationID, "inventory", "stock.consumed", map[string]any{
		"order_id":     resv.OrderID.String(),
		"consumed_at":  now.UTC().Format(time.RFC3339),
		"items_count":  len(resv.Items),
	})

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("stock: commit consume: %w", err)
	}

	s.log.Info("reservation consumed",
		zap.String("reservation_id", reservationID.String()),
	)
	return nil
}

// RecordConsumption records direct stock consumption without a prior reservation.
func (s *Service) RecordConsumption(ctx context.Context, tenantID uuid.UUID, req ConsumptionRequest) (*ConsumptionResponse, error) {
	whID, err := s.resolveWarehouseID(ctx, tenantID, req.WarehouseID)
	if err != nil {
		return nil, err
	}

	if req.IdempotencyKey != "" {
		existing, idempErr := s.client.Consumption.Query().
			Where(entconsumption.IdempotencyKeyEQ(req.IdempotencyKey)).
			First(ctx)
		if idempErr == nil {
			return &ConsumptionResponse{
				ID:          existing.ID,
				TenantID:    existing.TenantID,
				OrderID:     existing.OrderID,
				Status:      existing.Status,
				ProcessedAt: existing.ProcessedAt,
			}, nil
		}
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Explode each requested SKU into its raw ingredients (mirrors CreateReservation) so
	// callers may pass a menu-item SKU and we consume the recipe BOM. A SKU with no recipe
	// passes through unchanged, so directly-stocked goods still deduct correctly. Without
	// this, POS sale backflush (which sends menu SKUs) deducted the menu item's own balance
	// instead of its ingredients.
	type consumeLine struct {
		sku string
		qty float64
	}
	flattened := make([]consumeLine, 0, len(req.Items))
	for _, ci := range req.Items {
		ingredients, isBOM := s.explodeBOM(ctx, tenantID, ci.SKU, ci.Quantity)
		if !isBOM {
			flattened = append(flattened, consumeLine{sku: ci.SKU, qty: ci.Quantity})
			continue
		}
		for _, ing := range ingredients {
			flattened = append(flattened, consumeLine{sku: ing.SKU, qty: ing.Quantity})
		}
	}

	consumptionItems := make([]entschema.ConsumptionItemJSON, len(flattened))
	for i, cl := range flattened {
		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(cl.sku)).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("stock: item not found: sku=%s: %w", cl.sku, err)
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)
		if err == nil {
			deduct := cl.qty // keep fractional — do not truncate sub-unit consumption
			updatedBal, updateErr := tx.InventoryBalance.UpdateOne(bal).
				SetOnHand(max(0, bal.OnHand-deduct)).
				SetAvailable(max(0, bal.Available-deduct)).
				Save(ctx)
			if updateErr != nil {
				return nil, fmt.Errorf("stock: update balance for sku=%s: %w", cl.sku, updateErr)
			}

			// Check for low stock after consumption
			s.checkAndPublishLowStock(ctx, tx, tenantID, itm, updatedBal, whID)
		}

		consumptionItems[i] = entschema.ConsumptionItemJSON{
			SKU:      cl.sku,
			Quantity: cl.qty,
		}
	}

	reason := req.Reason
	if reason == "" {
		reason = "sale"
	}

	now := time.Now()
	builder := tx.Consumption.Create().
		SetTenantID(tenantID).
		SetOrderID(req.OrderID).
		SetItems(consumptionItems).
		SetReason(reason).
		SetStatus("processed").
		SetProcessedAt(now)

	if whID != uuid.Nil {
		builder.SetWarehouseID(whID)
	}
	if req.IdempotencyKey != "" {
		builder.SetIdempotencyKey(req.IdempotencyKey)
	}

	cons, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: create consumption: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit consumption: %w", err)
	}

	s.log.Info("consumption recorded",
		zap.String("consumption_id", cons.ID.String()),
		zap.String("order_id", req.OrderID.String()),
	)

	return &ConsumptionResponse{
		ID:          cons.ID,
		TenantID:    cons.TenantID,
		OrderID:     cons.OrderID,
		Status:      cons.Status,
		ProcessedAt: cons.ProcessedAt,
	}, nil
}

// writeOutboxEvent stores a domain event in the outbox within an Ent transaction.
// Non-fatal: logs on failure so the business operation still succeeds.
func (s *Service) writeOutboxEvent(ctx context.Context, tx *ent.Tx, tenantID, aggregateID uuid.UUID, aggregateType, eventType string, payload map[string]any) {
	evt := eventslib.NewEvent(eventType, aggregateType, aggregateID, tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		s.log.Warn("outbox: marshal event", zap.Error(err), zap.String("event_type", eventType))
		return
	}
	_, err = tx.OutboxEvent.Create().
		SetTenantID(tenantID).
		SetAggregateType(aggregateType).
		SetAggregateID(aggregateID.String()).
		SetEventType(eventType).
		SetPayload(json.RawMessage(data)).
		Save(ctx)
	if err != nil {
		s.log.Warn("outbox: write event", zap.Error(err), zap.String("event_type", eventType))
	}
}

// RestockItem represents a single item to restock (reverse consumption).
type RestockItem struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// RestockItems restores stock for returned items, incrementing on_hand and available.
// Used by return/refund consumers to restock the warehouse after a customer return.
func (s *Service) RestockItems(ctx context.Context, tenantID, warehouseID uuid.UUID, items []RestockItem, idempotencyKey string) error {
	whID, err := s.resolveWarehouseID(ctx, tenantID, warehouseID)
	if err != nil {
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("stock: begin restock tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, ri := range items {
		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(ri.SKU)).
			Only(ctx)
		if err != nil {
			s.log.Warn("restock: item not found, skipping", zap.String("sku", ri.SKU))
			continue
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)
		if err != nil {
			s.log.Warn("restock: no balance row, skipping", zap.String("sku", ri.SKU))
			continue
		}

		qty := ri.Quantity // fractional-capable restock
		_, err = tx.InventoryBalance.UpdateOne(bal).
			SetOnHand(bal.OnHand + qty).
			SetAvailable(bal.Available + qty).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("stock: restock balance sku=%s: %w", ri.SKU, err)
		}

		// Cascade: unblock recipe items when all their ingredients are back in stock.
		s.cascadeIngredientRestocked(ctx, tx, tenantID, itm.ID, whID)

		s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.restocked", map[string]any{
			"tenant_id":    tenantID.String(),
			"item_id":      itm.ID.String(),
			"sku":          ri.SKU,
			"quantity":     qty,
			"warehouse_id": whID.String(),
			"reason":       "customer_return",
		})
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("stock: commit restock: %w", err)
	}

	s.log.Info("items restocked",
		zap.Int("count", len(items)),
		zap.String("idempotency_key", idempotencyKey),
	)
	return nil
}

func (s *Service) mapReservation(r *ent.Reservation) *ReservationResponse {
	resp := &ReservationResponse{
		ID:        r.ID,
		TenantID:  r.TenantID,
		OrderID:   r.OrderID,
		Status:    r.Status,
		ExpiresAt: r.ExpiresAt,
		ConfirmedAt: r.ConfirmedAt,
		CreatedAt: r.CreatedAt,
	}

	resp.Items = make([]ReservedItem, len(r.Items))
	for i, ri := range r.Items {
		resp.Items[i] = ReservedItem{
			SKU:             ri.SKU,
			RequestedQty:    ri.RequestedQty,
			ReservedQty:     ri.ReservedQty,
			AvailableQty:    ri.AvailableQty,
			IsFullyReserved: ri.IsFullyReserved,
		}
	}

	return resp
}
