package transfers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/units"
	platformevents "github.com/bengobox/inventory-service/internal/platform/events"
)

// StockCascader is implemented by stock.Service (wired via WithStockCascade). Defined narrowly
// here rather than importing the stock package's own type, so a transfer's ship/receive/cancel
// balance mutations trigger the exact same real-time downstream sync as every other stock
// mutation path (AdjustStock, goods receipts, consumption) — POS/ordering catalog cache
// invalidation, inventory-ui's live WebSocket push, low-stock alerts, and recipe-ingredient
// depletion/restock cascades — instead of writing InventoryBalance silently.
type StockCascader interface {
	EmitStockChangeCascade(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, qtyBefore, qtyAfter float64, reason string)
}

// Service handles stock transfer business logic.
type Service struct {
	client *ent.Client
	log    *zap.Logger
	// seq, when wired, mints transfer numbers through the tenant-configurable document sequence
	// (numeric by default) instead of the legacy ad-hoc TRF-YYYYMMDD-NNNN count.
	seq *documents.SequenceService
	// stockCascade, when wired, is notified of every balance mutation this service makes.
	stockCascade StockCascader
}

// NewService creates a new transfers service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("transfers.service"),
	}
}

// WithSequence wires the document-sequence service so stock-transfer numbers are minted through
// the tenant's stock_transfer sequence (numeric by default), falling back to the legacy count.
func (s *Service) WithSequence(seq *documents.SequenceService) *Service {
	s.seq = seq
	return s
}

// WithStockCascade wires the real-time downstream sync (see StockCascader) into every
// ship/receive/cancel balance mutation this service makes.
func (s *Service) WithStockCascade(sc StockCascader) *Service {
	s.stockCascade = sc
	return s
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

	// Reject a fractional quantity for any discrete/count-based item (e.g. "2.5 phones") before
	// creating anything — covers this endpoint's manual "New Transfer" form AND MoveStockDialog's
	// quick-move flow, both of which post through here. Unit lookup failures fail open (no unit
	// info to judge against) rather than blocking a transfer over unrelated data gaps.
	itemIDs := make([]uuid.UUID, len(req.Items))
	for i, ln := range req.Items {
		itemIDs[i] = ln.ItemID
	}
	lineItems, err := tx.Item.Query().Where(item.IDIn(itemIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: load items for validation: %w", err)
	}
	unitIDByItem := make(map[uuid.UUID]*uuid.UUID, len(lineItems))
	for _, it := range lineItems {
		unitIDByItem[it.ID] = it.UnitID
	}
	unitCache := make(map[uuid.UUID]*ent.Unit)
	for _, ln := range req.Items {
		unitID := unitIDByItem[ln.ItemID]
		if unitID == nil {
			continue
		}
		u, ok := unitCache[*unitID]
		if !ok {
			var uErr error
			u, uErr = tx.Unit.Get(ctx, *unitID)
			if uErr != nil {
				continue
			}
			unitCache[*unitID] = u
		}
		if vErr := units.ValidateQuantityForUnit(ln.Quantity, u.Type, u.Name); vErr != nil {
			err = fmt.Errorf("transfers: %w", vErr)
			return nil, err
		}
	}

	// Hard-block over-quantity at CREATION time, not just at Ship. The real over-transfer guard
	// previously only lived in adjustBalance (called from ShipTransfer), so a draft transfer for
	// more than what's actually on hand at the source warehouse could be created and would only
	// fail much later when someone tried to ship it — reusing the same InventoryBalance.Available
	// check here closes that gap.
	srcBalances, err := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.WarehouseID(req.SourceWarehouseID), inventorybalance.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: query source balances: %w", err)
	}
	availableByItem := make(map[uuid.UUID]float64, len(srcBalances))
	for _, b := range srcBalances {
		availableByItem[b.ItemID] = b.Available
	}
	itemNameByID := make(map[uuid.UUID]string, len(lineItems))
	for _, it := range lineItems {
		itemNameByID[it.ID] = it.Name
	}
	for _, ln := range req.Items {
		if available := availableByItem[ln.ItemID]; ln.Quantity > available {
			err = fmt.Errorf("transfers: insufficient stock for %q at source warehouse (available %g, requested %g)",
				itemNameByID[ln.ItemID], available, ln.Quantity)
			return nil, err
		}
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
		SetReferenceNo(req.ReferenceNo).
		SetShippingCharges(req.ShippingCharges).
		SetCarrier(req.Carrier).
		SetFreightNotes(req.FreightNotes).
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
	s.writeOutboxEvent(ctx, tx, tenantID, transfer.ID, "inventory", "transfer.created", map[string]any{
		"transfer_id":      transfer.ID.String(),
		"transfer_number":  transferNumber,
		"tenant_id":        tenantID.String(),
		"status":           "draft",
		"reference_no":     req.ReferenceNo,
		"shipping_charges": req.ShippingCharges,
		"carrier":          req.Carrier,
		"freight_notes":    req.FreightNotes,
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

	// Reuses lineItems (already fetched above for the whole-unit-quantity validation) instead
	// of querying items again just to build the display map.
	itemInfo := make(map[uuid.UUID]itemDisplay, len(lineItems))
	for _, it := range lineItems {
		itemInfo[it.ID] = itemDisplay{name: it.Name, sku: it.Sku}
	}

	return s.buildTransferResponse(transfer, lines, srcWH, destWH, itemInfo), nil
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
	if !filter.From.IsZero() {
		q = q.Where(stocktransfer.CreatedAtGTE(filter.From))
	}
	if !filter.To.IsZero() {
		q = q.Where(stocktransfer.CreatedAtLTE(filter.To))
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

	itemIDs := make([]uuid.UUID, len(transfer.Edges.Lines))
	for i, l := range transfer.Edges.Lines {
		itemIDs[i] = l.ItemID
	}
	itemInfo := s.itemDisplayMap(ctx, s.client, itemIDs)

	return s.buildTransferResponse(transfer, transfer.Edges.Lines, srcWH, destWH, itemInfo), nil
}

// ShipTransfer transitions a transfer from draft to in_transit and moves the stock OUT
// of the source warehouse (it is now in transit between warehouses).
func (s *Service) ShipTransfer(ctx context.Context, tenantID, transferID uuid.UUID) error {
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

	if transfer.Status != stocktransfer.StatusDraft {
		return fmt.Errorf("transfers: can only ship a draft transfer, current status: %s", transfer.Status)
	}

	// Deduct each line's quantity from the SOURCE warehouse balance.
	for _, line := range transfer.Edges.Lines {
		if err = s.adjustBalance(ctx, tx, tenantID, line.ItemID, transfer.SourceWarehouseID, -line.Quantity); err != nil {
			return err
		}
	}

	now := time.Now()
	if _, err = tx.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusInTransit).
		SetShippedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("transfer shipped",
		zap.String("transfer_id", transferID.String()),
	)
	return nil
}

// adjustBalance applies delta to an item's on_hand+available at a warehouse within tx.
// A negative delta requires sufficient available stock; a positive delta creates the
// balance row when the destination warehouse has none yet. Notifies the wired StockCascader
// (if any) of the before/after available quantity so POS/ordering catalog overrides,
// inventory-ui's live push, low-stock alerts and recipe cascades stay in sync in real time —
// see StockCascader's doc comment.
func (s *Service) adjustBalance(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, delta float64) error {
	bal, err := tx.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itemID),
			inventorybalance.WarehouseID(warehouseID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		if delta < 0 {
			return fmt.Errorf("transfers: no stock for item %s at source warehouse", itemID)
		}
		if _, cErr := tx.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(itemID).
			SetWarehouseID(warehouseID).
			SetOnHand(delta).
			SetAvailable(delta).
			Save(ctx); cErr != nil {
			return cErr
		}
		s.emitCascade(ctx, tx, tenantID, itemID, warehouseID, 0, delta)
		return nil
	}
	if err != nil {
		return fmt.Errorf("transfers: query balance: %w", err)
	}
	if delta < 0 && bal.Available < -delta {
		return fmt.Errorf("transfers: insufficient stock for item %s at source (available %g, need %g)", itemID, bal.Available, -delta)
	}
	before := bal.Available
	after := before + delta
	update := tx.InventoryBalance.UpdateOne(bal).
		SetOnHand(bal.OnHand + delta).
		SetAvailable(after)
	// A shipment that fully depletes a warehouse's balance is a deliberate "moved/removed from
	// this location" event, distinct from an organic sale down to zero (sales never call this
	// function) — mark it so the item stops appearing in this outlet's catalog. Any increment
	// (receive at the destination, or a cancel restoring the source) means stock is present
	// again, so it always clears the flag.
	switch {
	case delta < 0 && after <= 0:
		update = update.SetRemovedFromLocation(true)
	case delta > 0:
		update = update.SetRemovedFromLocation(false)
	}
	if _, err = update.Save(ctx); err != nil {
		return err
	}
	s.emitCascade(ctx, tx, tenantID, itemID, warehouseID, before, after)
	return nil
}

// emitCascade notifies the wired StockCascader of a balance mutation, when one is wired.
// Best-effort — a nil stockCascade (e.g. in a test double) is a silent no-op, never a failure.
func (s *Service) emitCascade(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, before, after float64) {
	if s.stockCascade == nil {
		return
	}
	s.stockCascade.EmitStockChangeCascade(ctx, tx, tenantID, itemID, warehouseID, before, after, "stock_transfer")
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

	// Credit each line's quantity to the DESTINATION warehouse balance (stock has arrived).
	for _, line := range transfer.Edges.Lines {
		if err = s.adjustBalance(ctx, tx, tenantID, line.ItemID, transfer.DestinationWarehouseID, line.Quantity); err != nil {
			return err
		}
	}

	now := time.Now()
	_, err = tx.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusReceived).
		SetReceivedAt(now).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	// Publish transfer.completed event. Carries shipping_charges so the treasury freight
	// subscriber can post a freight expense (only when > 0; idempotent on transfer_id).
	s.writeOutboxEvent(ctx, tx, tenantID, transfer.ID, "inventory", "transfer.completed", map[string]any{
		"transfer_id":              transfer.ID.String(),
		"transfer_number":          transfer.TransferNumber,
		"tenant_id":                tenantID.String(),
		"source_warehouse_id":      transfer.SourceWarehouseID.String(),
		"destination_warehouse_id": transfer.DestinationWarehouseID.String(),
		"received_at":              now.UTC().Format(time.RFC3339),
		"reference_no":             transfer.ReferenceNo,
		"shipping_charges":         transfer.ShippingCharges,
		"carrier":                  transfer.Carrier,
		"freight_notes":            transfer.FreightNotes,
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

	if transfer.Status != stocktransfer.StatusDraft && transfer.Status != stocktransfer.StatusInTransit {
		return fmt.Errorf("transfers: can only cancel a draft or in-transit transfer, current status: %s", transfer.Status)
	}

	// If the transfer was already shipped, its stock left the source warehouse — restore it.
	if transfer.Status == stocktransfer.StatusInTransit {
		for _, line := range transfer.Edges.Lines {
			if err = s.adjustBalance(ctx, tx, tenantID, line.ItemID, transfer.SourceWarehouseID, line.Quantity); err != nil {
				return err
			}
		}
	}

	if _, err = tx.StockTransfer.UpdateOne(transfer).
		SetStatus(stocktransfer.StatusCancelled).
		Save(ctx); err != nil {
		return fmt.Errorf("transfers: update status: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("transfer cancelled",
		zap.String("transfer_id", transferID.String()),
	)
	return nil
}

// generateTransferNumber generates a unique transfer number for a tenant.
// Format: TRF-{YYYYMMDD}-{seq}
func (s *Service) generateTransferNumber(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID) (string, error) {
	if s.seq != nil {
		if n, err := s.seq.GenerateNumber(ctx, tenantID, documents.DocTypeStockTransfer); err == nil && n != "" {
			return n, nil
		}
	}
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

// itemDisplay carries the display fields (name, sku) a transfer line response needs — looked
// up once per Item ID by the caller (batch, not a query per line) and handed to
// buildTransferResponse.
type itemDisplay struct {
	name, sku string
}

// itemDisplayMap batch-loads name/sku for a set of item IDs. Missing/unresolvable items are
// simply absent from the map (buildTransferResponse leaves their line's name/sku blank rather
// than failing the whole response — an item transferred and later hard-deleted must never break
// the historical transfer record).
func (s *Service) itemDisplayMap(ctx context.Context, client *ent.Client, ids []uuid.UUID) map[uuid.UUID]itemDisplay {
	out := make(map[uuid.UUID]itemDisplay, len(ids))
	if len(ids) == 0 {
		return out
	}
	items, err := client.Item.Query().Where(item.IDIn(ids...)).All(ctx)
	if err != nil {
		s.log.Warn("transfers: load item display info failed", zap.Error(err))
		return out
	}
	for _, it := range items {
		out[it.ID] = itemDisplay{name: it.Name, sku: it.Sku}
	}
	return out
}

// buildTransferResponse maps ent entities to a TransferResponse.
func (s *Service) buildTransferResponse(transfer *ent.StockTransfer, lines []*ent.StockTransferLine, srcWH, destWH *ent.Warehouse, itemInfo map[uuid.UUID]itemDisplay) *TransferResponse {
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
		InitiatedBy:     transfer.InitiatedBy,
		Notes:           transfer.Notes,
		ReferenceNo:     transfer.ReferenceNo,
		ShippingCharges: transfer.ShippingCharges,
		Carrier:         transfer.Carrier,
		FreightNotes:    transfer.FreightNotes,
		ShippedAt:       transfer.ShippedAt,
		ReceivedAt:      transfer.ReceivedAt,
		CreatedAt:       transfer.CreatedAt,
		UpdatedAt:       transfer.UpdatedAt,
	}

	received := transfer.Status == stocktransfer.StatusReceived
	resp.Lines = make([]TransferLineResponse, len(lines))
	for i, l := range lines {
		info := itemInfo[l.ItemID]
		line := TransferLineResponse{
			ID:        l.ID,
			ItemID:    l.ItemID,
			ItemName:  info.name,
			ItemSKU:   info.sku,
			VariantID: l.VariantID,
			LotID:     l.LotID,
			Quantity:  l.Quantity,
		}
		if received {
			qty := l.Quantity
			line.ReceivedQty = &qty
		}
		resp.Lines[i] = line
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
	platformevents.WriteOutboxTx(ctx, tx, s.log, tenantID, aggregateID, aggregateType, eventType, payload)
}
