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

// ReservationItem represents a single item to reserve.
type ReservationItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
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
	SKU             string `json:"sku"`
	RequestedQty    int    `json:"requested_qty"`
	ReservedQty     int    `json:"reserved_qty"`
	AvailableQty    int    `json:"available_qty"`
	IsFullyReserved bool   `json:"is_fully_reserved"`
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
	SKU         string    `json:"sku"`
	Adjustment  int       `json:"adjustment"`
	Reason      string    `json:"reason"`
	Reference   string    `json:"reference,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	AdjustedBy  uuid.UUID `json:"adjusted_by"`
	WarehouseID uuid.UUID `json:"warehouse_id,omitempty"`
}

// AdjustStockResponse represents the result of a stock adjustment.
type AdjustStockResponse struct {
	ID           uuid.UUID `json:"id"`
	SKU          string    `json:"sku"`
	OnHand       int       `json:"on_hand"`
	Available    int       `json:"available"`
	Reserved     int       `json:"reserved"`
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
	WarehouseID    uuid.UUID `json:"warehouse_id"`
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
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("stock: no balance found for sku=%s", req.SKU)
		}
		return nil, fmt.Errorf("stock: query balance: %w", err)
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

	updatedBal, err := tx.InventoryBalance.UpdateOne(bal).
		SetOnHand(newOnHand).
		SetAvailable(newAvailable).
		Save(ctx)
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

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("stock: commit adjustment: %w", err)
	}

	s.log.Info("stock adjusted",
		zap.String("sku", req.SKU),
		zap.Int("adjustment", req.Adjustment),
		zap.String("reason", req.Reason),
		zap.Int("new_on_hand", newOnHand),
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

	result := make([]StockAdjustmentDTO, len(adjustments))
	for i, a := range adjustments {
		result[i] = StockAdjustmentDTO{
			ID:             a.ID,
			TenantID:       a.TenantID,
			ItemID:         a.ItemID,
			WarehouseID:    a.WarehouseID,
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
	if bal.Available <= 0 {
		s.writeOutboxEvent(ctx, tx, tenantID, itm.ID, "inventory", "stock.out", map[string]any{
			"tenant_id":    tenantID.String(),
			"item_id":      itm.ID.String(),
			"sku":          itm.Sku,
			"name":         itm.Name,
			"available":    bal.Available,
			"warehouse_id": warehouseID.String(),
			"notification": map[string]any{
				"target": "staff",
			},
		})
		s.log.Warn("stock-out alert published",
			zap.String("sku", itm.Sku),
			zap.Int("available", bal.Available),
		)
	} else if bal.Available <= bal.ReorderLevel {
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
			zap.Int("available", bal.Available),
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
			return uuid.Nil, fmt.Errorf("stock: no default warehouse for tenant")
		}
		return uuid.Nil, fmt.Errorf("stock: query default warehouse: %w", err)
	}
	return wh.ID, nil
}

// CreateReservation reserves stock for an order within a transaction.
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
		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(ri.SKU), item.IsActive(true)).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("stock: item not found: sku=%s: %w", ri.SKU, err)
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)

		var availableQty int
		if err != nil {
			if ent.IsNotFound(err) {
				availableQty = 0
			} else {
				return nil, fmt.Errorf("stock: query balance: %w", err)
			}
		} else {
			availableQty = bal.Available
		}

		reserveQty := ri.Quantity
		if reserveQty > availableQty {
			reserveQty = availableQty
		}

		if bal != nil && reserveQty > 0 {
			_, err = tx.InventoryBalance.UpdateOne(bal).
				SetAvailable(bal.Available - reserveQty).
				SetReserved(bal.Reserved + reserveQty).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("stock: update balance for sku=%s: %w", ri.SKU, err)
			}
		}

		reservedItems = append(reservedItems, entschema.ReservedItemJSON{
			SKU:             ri.SKU,
			RequestedQty:    ri.Quantity,
			ReservedQty:     reserveQty,
			AvailableQty:    availableQty,
			IsFullyReserved: reserveQty >= ri.Quantity,
		})
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

	consumptionItems := make([]entschema.ConsumptionItemJSON, len(req.Items))
	for i, ci := range req.Items {
		itm, err := tx.Item.Query().
			Where(item.TenantID(tenantID), item.Sku(ci.SKU)).
			Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("stock: item not found: sku=%s: %w", ci.SKU, err)
		}

		bal, err := tx.InventoryBalance.Query().
			Where(
				inventorybalance.TenantID(tenantID),
				inventorybalance.ItemID(itm.ID),
				inventorybalance.WarehouseID(whID),
			).
			First(ctx)
		if err == nil {
			deduct := int(ci.Quantity)
			updatedBal, updateErr := tx.InventoryBalance.UpdateOne(bal).
				SetOnHand(max(0, bal.OnHand-deduct)).
				SetAvailable(max(0, bal.Available-deduct)).
				Save(ctx)
			if updateErr != nil {
				return nil, fmt.Errorf("stock: update balance for sku=%s: %w", ci.SKU, updateErr)
			}

			// Check for low stock after consumption
			s.checkAndPublishLowStock(ctx, tx, tenantID, itm, updatedBal, whID)
		}

		consumptionItems[i] = entschema.ConsumptionItemJSON{
			SKU:      ci.SKU,
			Quantity: ci.Quantity,
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
	data, err := json.Marshal(evt)
	if err != nil {
		s.log.Warn("outbox: marshal event", zap.Error(err), zap.String("event_type", eventType))
		return
	}
	_, err = tx.OutboxEvent.Create().
		SetTenantID(tenantID).
		SetAggregateType(aggregateType).
		SetAggregateID(aggregateID.String()).
		SetEventType(eventType).
		SetPayload(data).
		Save(ctx)
	if err != nil {
		s.log.Warn("outbox: write event", zap.Error(err), zap.String("event_type", eventType))
	}
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
