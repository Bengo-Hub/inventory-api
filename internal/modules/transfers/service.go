package transfers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Service handles stock transfer business logic.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new transfers service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("transfers.service"),
	}
}

// CreateTransfer creates a new stock transfer in draft status.
func (s *Service) CreateTransfer(ctx context.Context, tenantID uuid.UUID, req CreateTransferRequest) (*TransferResponse, error) {
	if req.SourceWarehouseID == req.DestinationWarehouseID {
		return nil, fmt.Errorf("transfers: source and destination warehouse must be different")
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("transfers: at least one item is required")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Validate warehouses exist and belong to tenant
	srcWH, err := tx.Warehouse.Query().
		Where(warehouse.ID(req.SourceWarehouseID), warehouse.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: source warehouse not found: %w", err)
	}

	destWH, err := tx.Warehouse.Query().
		Where(warehouse.ID(req.DestinationWarehouseID), warehouse.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: destination warehouse not found: %w", err)
	}

	// Generate transfer number: TRF-{YYYYMMDD}-{seq}
	transferNumber, err := s.generateTransferNumber(ctx, tx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("transfers: generate number: %w", err)
	}

	// Create the transfer
	transfer, err := tx.StockTransfer.Create().
		SetTenantID(tenantID).
		SetSourceWarehouseID(req.SourceWarehouseID).
		SetDestinationWarehouseID(req.DestinationWarehouseID).
		SetTransferNumber(transferNumber).
		SetStatus(stocktransfer.StatusDraft).
		SetNotes(req.Notes).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: create transfer: %w", err)
	}

	// Create transfer lines
	lines := make([]*ent.StockTransferLine, 0, len(req.Items))
	for _, item := range req.Items {
		lineBuilder := tx.StockTransferLine.Create().
			SetTransferID(transfer.ID).
			SetItemID(item.ItemID).
			SetQuantity(item.Quantity).
			SetNillableVariantID(item.VariantID).
			SetNillableLotID(item.LotID)

		line, err := lineBuilder.Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("transfers: create transfer line: %w", err)
		}
		lines = append(lines, line)
	}

	// Publish transfer.created event via outbox
	s.writeOutboxEvent(ctx, tx, tenantID, transfer.ID, "inventory", "inventory.transfer.created", map[string]any{
		"transfer_id":     transfer.ID.String(),
		"transfer_number": transferNumber,
		"tenant_id":       tenantID.String(),
		"status":          "draft",
		"from_warehouse": map[string]any{
			"id":        srcWH.ID.String(),
			"name":      srcWH.Name,
			"code":      srcWH.Code,
			"address":   srcWH.Address,
			"latitude":  srcWH.Latitude,
			"longitude": srcWH.Longitude,
		},
		"to_warehouse": map[string]any{
			"id":        destWH.ID.String(),
			"name":      destWH.Name,
			"code":      destWH.Code,
			"address":   destWH.Address,
			"latitude":  destWH.Latitude,
			"longitude": destWH.Longitude,
		},
		"items": s.buildLineItems(lines),
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("transfer created",
		zap.String("transfer_id", transfer.ID.String()),
		zap.String("transfer_number", transferNumber),
	)

	return s.buildTransferResponse(transfer, lines, srcWH, destWH), nil
}

// ListTransfers returns transfers for a tenant with optional filters.
func (s *Service) ListTransfers(ctx context.Context, tenantID uuid.UUID, filter TransferListFilter) ([]TransferSummary, int, error) {
	q := s.client.StockTransfer.Query().
		Where(stocktransfer.TenantID(tenantID))

	if filter.Status != "" {
		status := stocktransfer.Status(filter.Status)
		if stocktransfer.StatusValidator(status) == nil {
			q = q.Where(stocktransfer.StatusEQ(status))
		}
	}
	if filter.Search != "" {
		q = q.Where(stocktransfer.TransferNumberContainsFold(filter.Search))
	}

	// Get total count before pagination
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("transfers: count: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	transfers, err := q.
		WithLines().
		Order(ent.Desc(stocktransfer.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("transfers: list: %w", err)
	}

	// Collect warehouse IDs for batch lookup
	whIDs := make(map[uuid.UUID]bool)
	for _, t := range transfers {
		whIDs[t.SourceWarehouseID] = true
		whIDs[t.DestinationWarehouseID] = true
	}

	whIDSlice := make([]uuid.UUID, 0, len(whIDs))
	for id := range whIDs {
		whIDSlice = append(whIDSlice, id)
	}

	warehouses, err := s.client.Warehouse.Query().
		Where(warehouse.IDIn(whIDSlice...)).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("transfers: query warehouses: %w", err)
	}

	whMap := make(map[uuid.UUID]*ent.Warehouse, len(warehouses))
	for _, w := range warehouses {
		whMap[w.ID] = w
	}

	result := make([]TransferSummary, len(transfers))
	for i, t := range transfers {
		srcName := ""
		if wh, ok := whMap[t.SourceWarehouseID]; ok {
			srcName = wh.Name
		}
		destName := ""
		if wh, ok := whMap[t.DestinationWarehouseID]; ok {
			destName = wh.Name
		}

		result[i] = TransferSummary{
			ID:                  t.ID,
			TransferNumber:      t.TransferNumber,
			Status:              string(t.Status),
			SourceWarehouseName: srcName,
			DestWarehouseName:   destName,
			LineCount:           len(t.Edges.Lines),
			ShippedAt:           t.ShippedAt,
			ReceivedAt:          t.ReceivedAt,
			CreatedAt:           t.CreatedAt,
		}
	}

	return result, total, nil
}

// GetTransfer returns a single transfer with full details.
func (s *Service) GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*TransferResponse, error) {
	transfer, err := s.client.StockTransfer.Query().
		Where(
			stocktransfer.ID(transferID),
			stocktransfer.TenantID(tenantID),
		).
		WithLines().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("transfers: transfer not found")
		}
		return nil, fmt.Errorf("transfers: query transfer: %w", err)
	}

	srcWH, err := s.client.Warehouse.Get(ctx, transfer.SourceWarehouseID)
	if err != nil {
		return nil, fmt.Errorf("transfers: source warehouse lookup: %w", err)
	}

	destWH, err := s.client.Warehouse.Get(ctx, transfer.DestinationWarehouseID)
	if err != nil {
		return nil, fmt.Errorf("transfers: destination warehouse lookup: %w", err)
	}

	return s.buildTransferResponse(transfer, transfer.Edges.Lines, srcWH, destWH), nil
}

// ShipTransfer transitions a transfer from draft to in_transit.
func (s *Service) ShipTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error {
	transfer, err := s.client.StockTransfer.Query().
		Where(
			stocktransfer.ID(transferID),
			stocktransfer.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("transfers: transfer not found")
		}
		return fmt.Errorf("transfers: query transfer: %w", err)
	}

	if transfer.Status != stocktransfer.StatusDraft {
		return fmt.Errorf("transfers: can only ship a draft transfer, current status: %s", transfer.Status)
	}

	now := time.Now()
	_, err = s.client.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusInTransit).
		SetShippedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	s.log.Info("transfer shipped",
		zap.String("transfer_id", transferID.String()),
	)
	return nil
}

// ReceiveTransfer transitions a transfer from in_transit to received.
func (s *Service) ReceiveTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("transfers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	transfer, err := tx.StockTransfer.Query().
		Where(
			stocktransfer.ID(transferID),
			stocktransfer.TenantID(tenantID),
		).
		WithLines().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("transfers: transfer not found")
		}
		return fmt.Errorf("transfers: query transfer: %w", err)
	}

	if transfer.Status != stocktransfer.StatusInTransit {
		return fmt.Errorf("transfers: can only receive an in-transit transfer, current status: %s", transfer.Status)
	}

	now := time.Now()
	_, err = tx.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusReceived).
		SetReceivedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	// Publish transfer.completed event
	s.writeOutboxEvent(ctx, tx, tenantID, transfer.ID, "inventory", "inventory.transfer.completed", map[string]any{
		"transfer_id":              transfer.ID.String(),
		"transfer_number":          transfer.TransferNumber,
		"tenant_id":                tenantID.String(),
		"source_warehouse_id":      transfer.SourceWarehouseID.String(),
		"destination_warehouse_id": transfer.DestinationWarehouseID.String(),
		"received_at":              now.UTC().Format(time.RFC3339),
		"items":                    s.buildLineItems(transfer.Edges.Lines),
	})

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("transfer received",
		zap.String("transfer_id", transferID.String()),
	)
	return nil
}

// CancelTransfer cancels a transfer that is in draft or in_transit status.
func (s *Service) CancelTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error {
	transfer, err := s.client.StockTransfer.Query().
		Where(
			stocktransfer.ID(transferID),
			stocktransfer.TenantID(tenantID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("transfers: transfer not found")
		}
		return fmt.Errorf("transfers: query transfer: %w", err)
	}

	if transfer.Status != stocktransfer.StatusDraft && transfer.Status != stocktransfer.StatusInTransit {
		return fmt.Errorf("transfers: can only cancel a draft or in-transit transfer, current status: %s", transfer.Status)
	}

	_, err = s.client.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusCancelled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	s.log.Info("transfer cancelled",
		zap.String("transfer_id", transferID.String()),
	)
	return nil
}

// generateTransferNumber generates a unique transfer number for a tenant.
// Format: TRF-{YYYYMMDD}-{seq}
func (s *Service) generateTransferNumber(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) (string, error) {
	today := time.Now().Format("20060102")
	prefix := fmt.Sprintf("TRF-%s-", today)

	// Count existing transfers for this tenant today to determine sequence
	count, err := tx.StockTransfer.Query().
		Where(
			stocktransfer.TenantID(tenantID),
			stocktransfer.TransferNumberHasPrefix(prefix),
		).
		Count(ctx)
	if err != nil {
		return "", fmt.Errorf("count transfers: %w", err)
	}

	return fmt.Sprintf("%s%04d", prefix, count+1), nil
}

// buildTransferResponse maps ent entities to a TransferResponse.
func (s *Service) buildTransferResponse(transfer *ent.StockTransfer, lines []*ent.StockTransferLine, srcWH, destWH *ent.Warehouse) *TransferResponse {
	resp := &TransferResponse{
		ID:             transfer.ID,
		TenantID:       transfer.TenantID,
		TransferNumber: transfer.TransferNumber,
		Status:         string(transfer.Status),
		SourceWarehouse: WarehouseInfo{
			ID:        srcWH.ID,
			Name:      srcWH.Name,
			Code:      srcWH.Code,
			Address:   srcWH.Address,
			Latitude:  srcWH.Latitude,
			Longitude: srcWH.Longitude,
		},
		DestinationWarehouse: WarehouseInfo{
			ID:        destWH.ID,
			Name:      destWH.Name,
			Code:      destWH.Code,
			Address:   destWH.Address,
			Latitude:  destWH.Latitude,
			Longitude: destWH.Longitude,
		},
		InitiatedBy: transfer.InitiatedBy,
		Notes:       transfer.Notes,
		ShippedAt:   transfer.ShippedAt,
		ReceivedAt:  transfer.ReceivedAt,
		CreatedAt:   transfer.CreatedAt,
		UpdatedAt:   transfer.UpdatedAt,
	}

	resp.Lines = make([]TransferLineResponse, len(lines))
	for i, l := range lines {
		resp.Lines[i] = TransferLineResponse{
			ID:        l.ID,
			ItemID:    l.ItemID,
			VariantID: l.VariantID,
			LotID:     l.LotID,
			Quantity:  l.Quantity,
		}
	}

	return resp
}

// buildLineItems converts transfer lines to a map slice for event payloads.
func (s *Service) buildLineItems(lines []*ent.StockTransferLine) []map[string]any {
	items := make([]map[string]any, len(lines))
	for i, l := range lines {
		item := map[string]any{
			"item_id":  l.ItemID.String(),
			"quantity": l.Quantity,
		}
		if l.VariantID != nil {
			item["variant_id"] = l.VariantID.String()
		}
		if l.LotID != nil {
			item["lot_id"] = l.LotID.String()
		}
		items[i] = item
	}
	return items
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
