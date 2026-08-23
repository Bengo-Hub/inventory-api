package transfers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
	"github.com/bengobox/inventory-service/internal/ent/stocktransferline"
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

// validateTransferLines rejects a fractional quantity for any discrete/count-based item (e.g.
// "2.5 phones") and hard-blocks over-quantity against the source warehouse's actual available
// stock. Shared by CreateTransfer and UpdateTransfer so an amended draft's quantities are held
// to the exact same bar as a brand-new transfer — this is the ONLY place either of them checks
// it, closing the gap where the real over-transfer guard used to live only in adjustBalance
// (called from ShipTransfer), letting a too-large draft exist and fail much later at Ship.
// Unit lookup failures fail open (no unit info to judge against) rather than blocking a
// transfer over an unrelated data gap. Returns the loaded Items (for building the response).
func (s *Service) validateTransferLines(ctx context.Context, tx *ent.Tx, tenantID, sourceWarehouseID uuid.UUID, items []TransferLineRequest) ([]*ent.Item, error) {
	itemIDs := make([]uuid.UUID, len(items))
	for i, ln := range items {
		itemIDs[i] = ln.ItemID
	}
	// WithUnits so the returned []*ent.Item can double as the display-info source (name/SKU/unit
	// abbreviation) for the response the caller builds right after validating — see itemDisplay.
	lineItems, err := tx.Item.Query().Where(item.IDIn(itemIDs...)).WithUnits().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: load items for validation: %w", err)
	}
	unitIDByItem := make(map[uuid.UUID]*uuid.UUID, len(lineItems))
	for _, it := range lineItems {
		unitIDByItem[it.ID] = it.UnitID
	}
	unitCache := make(map[uuid.UUID]*ent.Unit)
	for _, ln := range items {
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
			return nil, fmt.Errorf("transfers: %w", vErr)
		}
	}

	srcBalances, err := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.WarehouseID(sourceWarehouseID), inventorybalance.ItemIDIn(itemIDs...)).
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
	for _, ln := range items {
		if available := availableByItem[ln.ItemID]; ln.Quantity > available {
			return nil, fmt.Errorf("transfers: insufficient stock for %q at source warehouse (available %g, requested %g)",
				itemNameByID[ln.ItemID], available, ln.Quantity)
		}
	}
	return lineItems, nil
}

// ErrInvalidTransferDate is returned when Create/UpdateTransfer's optional transfer_date field
// fails parseAndValidateTransferDate (malformed, or outside the accepted window). Handlers map
// this to 400 instead of an opaque 500.
var ErrInvalidTransferDate = errors.New("invalid transfer_date")

// maxTransferDateBackDays/maxTransferDateForwardDays bound transfer_date on EITHER side of today
// (unlike pos-api's business_date, which is backdate-only) — a tenant may log a transfer days
// after the stock actually moved, or schedule one ahead for a planned future shipment (client
// request: "WEKA DATE... back date au tupeleke date mbele" — back-date or push the date
// forward). A year of slack in each direction comfortably covers legitimate corrections/planning
// while still rejecting an obvious typo (e.g. a wrong year).
const (
	maxTransferDateBackDays    = 366
	maxTransferDateForwardDays = 366
)

// parseAndValidateTransferDate parses a "YYYY-MM-DD" calendar day (UTC — inventory-api has no
// per-tenant timezone concept, matching parseCreatedAtRange's existing convention) and bounds it
// to maxTransferDateBackDays/maxTransferDateForwardDays on either side of today.
func parseAndValidateTransferDate(dateStr string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", strings.TrimSpace(dateStr))
	if err != nil {
		return time.Time{}, fmt.Errorf("transfer_date must be YYYY-MM-DD")
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if d.After(today.AddDate(0, 0, maxTransferDateForwardDays)) {
		return time.Time{}, fmt.Errorf("transfer_date is too far in the future (more than %d days after today)", maxTransferDateForwardDays)
	}
	if d.Before(today.AddDate(0, 0, -maxTransferDateBackDays)) {
		return time.Time{}, fmt.Errorf("transfer_date is too far in the past (more than %d days before today)", maxTransferDateBackDays)
	}
	return d, nil
}

// resolveTransferDate parses and validates req's optional transfer_date string, returning nil
// when it's blank (report under created_at, the pre-existing default). Shared by Create and
// UpdateTransfer so both enforce the identical contract.
func resolveTransferDate(dateStr string) (*time.Time, error) {
	if strings.TrimSpace(dateStr) == "" {
		return nil, nil
	}
	parsed, err := parseAndValidateTransferDate(dateStr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidTransferDate, err.Error())
	}
	return &parsed, nil
}

// EffectiveTransferDate returns the calendar day a transfer counts toward in reports: the
// user-set transfer_date override when present, else CreatedAt (server ingestion time). List/
// report queries that need to honor a backdated/postdated transfer should filter on this
// precedence rather than raw CreatedAt — see ListTransfers.
func EffectiveTransferDate(t *ent.StockTransfer) time.Time {
	if t.TransferDate != nil {
		return *t.TransferDate
	}
	return t.CreatedAt
}

// orderByEffectiveDate sorts StockTransfer rows by the same effective date the ListTransfers
// from/to filter and EffectiveTransferDate use (transfer_date when set, else created_at),
// descending by default — so a backdated/postdated transfer sorts into its true chronological
// position among the rows actually entered that day, instead of always floating to the top of
// the list under its real entry date. Needs a raw SQL expression because ent's generated Asc/Desc
// only order by a single column, not a COALESCE across two. A secondary "created_at DESC" term
// keeps same-effective-date rows in a stable, real-entry-order tiebreak. Mirrors pos-api's
// identically-named handlers.orderByEffectiveDate for POSOrder.business_date.
func orderByEffectiveDate(desc bool) stocktransfer.OrderOption {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return func(s *sql.Selector) {
		expr := "COALESCE(" + s.C(stocktransfer.FieldTransferDate) + ", " + s.C(stocktransfer.FieldCreatedAt) + ")"
		s.OrderExprFunc(func(b *sql.Builder) {
			b.WriteString(expr + " " + dir + ", " + s.C(stocktransfer.FieldCreatedAt) + " " + dir)
		})
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
	// Fail fast, before opening the transaction, on a malformed or out-of-range date.
	transferDate, err := resolveTransferDate(req.TransferDate)
	if err != nil {
		return nil, err
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

	// Validate quantities (whole-unit + over-quantity against source availability) — shared with
	// UpdateTransfer so an amended draft is held to the exact same bar as a brand-new one.
	lineItems, err := s.validateTransferLines(ctx, tx, tenantID, req.SourceWarehouseID, req.Items)
	if err != nil {
		return nil, err
	}

	// Transfer number is dated to the transfer's own effective date when backdated/postdated
	// (transferDate), so it reads consistently with the Date column instead of always today.
	numberDate := time.Now()
	if transferDate != nil {
		numberDate = *transferDate
	}
	transferNumber, err := s.generateTransferNumber(ctx, tx, tenantID, numberDate)
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
		SetNillableTransferDate(transferDate).
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
		itemInfo[it.ID] = itemDisplay{name: it.Name, sku: it.Sku, unit: unitAbbrev(it)}
	}

	return s.buildTransferResponse(transfer, lines, srcWH, destWH, itemInfo), nil
}

// UpdateTransfer amends a DRAFT transfer's line items and header fields before it ships — the
// tool for correcting a transfer whose drafted quantities don't match physical stock (some
// items missing, a count discrepancy) without cancelling and recreating the whole record (which
// would lose the transfer number, freight/carrier info, and audit continuity). Only allowed
// while status is still "draft": once shipped, the drafted lines are exactly what was deducted
// from the source warehouse, so editing after that point would silently desync the record from
// reality. Source/destination warehouse are immutable here — the rare wrong-warehouse case is
// still served by Cancel + a fresh Create. Replaces the line set wholesale (simplest correct
// semantics for "here's what it actually is"), reusing the exact same validation CreateTransfer
// applies to a brand-new transfer.
func (s *Service) UpdateTransfer(ctx context.Context, tenantID, transferID uuid.UUID, req UpdateTransferRequest) (*TransferResponse, error) {
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("transfers: at least one item is required")
	}
	transferDate, err := resolveTransferDate(req.TransferDate)
	if err != nil {
		return nil, err
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

	transfer, err := tx.StockTransfer.Query().
		Where(stocktransfer.ID(transferID), stocktransfer.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("transfers: transfer not found")
		}
		return nil, fmt.Errorf("transfers: query transfer: %w", err)
	}
	if transfer.Status != stocktransfer.StatusDraft {
		return nil, fmt.Errorf("transfers: can only amend a draft transfer, current status: %s", transfer.Status)
	}

	lineItems, err := s.validateTransferLines(ctx, tx, tenantID, transfer.SourceWarehouseID, req.Items)
	if err != nil {
		return nil, err
	}

	if _, err = tx.StockTransferLine.Delete().Where(stocktransferline.TransferID(transferID)).Exec(ctx); err != nil {
		return nil, fmt.Errorf("transfers: clear existing lines: %w", err)
	}
	lines := make([]*ent.StockTransferLine, 0, len(req.Items))
	for _, ln := range req.Items {
		line, lerr := tx.StockTransferLine.Create().
			SetTransferID(transferID).
			SetItemID(ln.ItemID).
			SetQuantity(ln.Quantity).
			SetNillableVariantID(ln.VariantID).
			SetNillableLotID(ln.LotID).
			Save(ctx)
		if lerr != nil {
			err = fmt.Errorf("transfers: create transfer line: %w", lerr)
			return nil, err
		}
		lines = append(lines, line)
	}

	updateBuilder := tx.StockTransfer.UpdateOne(transfer).
		SetNotes(req.Notes).
		SetReferenceNo(req.ReferenceNo).
		SetShippingCharges(req.ShippingCharges).
		SetCarrier(req.Carrier).
		SetFreightNotes(req.FreightNotes)
	// Full-replace, like every other header field on this request: a blank transfer_date clears
	// the override back to reporting under created_at instead of leaving a stale value in place.
	if transferDate != nil {
		updateBuilder = updateBuilder.SetTransferDate(*transferDate)
	} else {
		updateBuilder = updateBuilder.ClearTransferDate()
	}
	updated, err := updateBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: update transfer: %w", err)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, transferID, "inventory", "transfer.updated", map[string]any{
		"transfer_id":     transferID.String(),
		"transfer_number": updated.TransferNumber,
		"tenant_id":       tenantID.String(),
		"items":           s.buildLineItems(lines),
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("transfer updated", zap.String("transfer_id", transferID.String()))

	srcWH, err := s.client.Warehouse.Get(ctx, updated.SourceWarehouseID)
	if err != nil {
		return nil, fmt.Errorf("transfers: source warehouse lookup: %w", err)
	}
	destWH, err := s.client.Warehouse.Get(ctx, updated.DestinationWarehouseID)
	if err != nil {
		return nil, fmt.Errorf("transfers: destination warehouse lookup: %w", err)
	}
	itemInfo := make(map[uuid.UUID]itemDisplay, len(lineItems))
	for _, it := range lineItems {
		itemInfo[it.ID] = itemDisplay{name: it.Name, sku: it.Sku, unit: unitAbbrev(it)}
	}

	return s.buildTransferResponse(updated, lines, srcWH, destWH, itemInfo), nil
}

// CompletedTransferLine is one item's already-moved quantity for RecordCompletedTransfer.
type CompletedTransferLine struct {
	ItemID   uuid.UUID
	Quantity float64
}

// RecordCompletedTransfer creates a StockTransfer + lines already in the terminal "received"
// state, for a warehouse-to-warehouse move that ALREADY happened atomically through another
// feature (e.g. a bulk stock adjustment whose lines specify a destination warehouse). Pure
// after-the-fact bookkeeping — it never touches InventoryBalance itself (the caller already did
// that) and never runs the over-quantity-vs-available check CreateTransfer does (irrelevant once
// the move is done). This is what gives every warehouse-to-warehouse move a transfer_number and a
// place in the Transfers list/reporting regardless of entry point, without forcing an unrelated
// feature through the draft/ship/approval workflow meant for a chosen, still-in-flight shipment —
// see [[stocktransfer.go]]'s `origin` field comment. Best-effort by design: the caller should log
// and continue on error rather than treat it as a failure of the (already-succeeded) move itself.
func (s *Service) RecordCompletedTransfer(ctx context.Context, tenantID, sourceWarehouseID, destWarehouseID uuid.UUID, origin string, lines []CompletedTransferLine, initiatedBy uuid.UUID, notes string) (*ent.StockTransfer, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("transfers: at least one line is required")
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

	now := time.Now()
	transferNumber, err := s.generateTransferNumber(ctx, tx, tenantID, now)
	if err != nil {
		return nil, fmt.Errorf("transfers: generate number: %w", err)
	}

	transferBuilder := tx.StockTransfer.Create().
		SetTenantID(tenantID).
		SetSourceWarehouseID(sourceWarehouseID).
		SetDestinationWarehouseID(destWarehouseID).
		SetTransferNumber(transferNumber).
		SetStatus(stocktransfer.StatusReceived).
		SetOrigin(origin).
		SetShippedAt(now).
		SetReceivedAt(now).
		SetNotes(notes)
	if initiatedBy != uuid.Nil {
		transferBuilder = transferBuilder.SetInitiatedBy(initiatedBy)
	}
	transfer, err := transferBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("transfers: create transfer: %w", err)
	}

	entLines := make([]*ent.StockTransferLine, 0, len(lines))
	for _, ln := range lines {
		line, lerr := tx.StockTransferLine.Create().
			SetTransferID(transfer.ID).
			SetItemID(ln.ItemID).
			SetQuantity(ln.Quantity).
			SetReceivedQuantity(ln.Quantity).
			Save(ctx)
		if lerr != nil {
			err = fmt.Errorf("transfers: create transfer line: %w", lerr)
			return nil, err
		}
		entLines = append(entLines, line)
	}

	s.writeOutboxEvent(ctx, tx, tenantID, transfer.ID, "inventory", "transfer.completed", map[string]any{
		"transfer_id":              transfer.ID.String(),
		"transfer_number":          transferNumber,
		"tenant_id":                tenantID.String(),
		"source_warehouse_id":      sourceWarehouseID.String(),
		"destination_warehouse_id": destWarehouseID.String(),
		"received_at":              now.UTC().Format(time.RFC3339),
		"origin":                   origin,
		"items":                    s.buildLineItems(entLines),
	})

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("transfers: commit: %w", err)
	}

	s.log.Info("completed transfer recorded",
		zap.String("transfer_id", transfer.ID.String()),
		zap.String("origin", origin),
	)
	return transfer, nil
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
	// Filter on the EFFECTIVE date (transfer_date override when set, else created_at) rather than
	// raw created_at — otherwise a backdated/postdated transfer would be correctly displayed under
	// its overridden date but silently excluded from (or wrongly included in) a from/to search for
	// that date, the exact display/filter mismatch pos-api hit with POSOrder.business_date. See
	// EffectiveTransferDate.
	if !filter.From.IsZero() {
		q = q.Where(stocktransfer.Or(
			stocktransfer.And(stocktransfer.TransferDateNotNil(), stocktransfer.TransferDateGTE(filter.From)),
			stocktransfer.And(stocktransfer.TransferDateIsNil(), stocktransfer.CreatedAtGTE(filter.From)),
		))
	}
	if !filter.To.IsZero() {
		q = q.Where(stocktransfer.Or(
			stocktransfer.And(stocktransfer.TransferDateNotNil(), stocktransfer.TransferDateLTE(filter.To)),
			stocktransfer.And(stocktransfer.TransferDateIsNil(), stocktransfer.CreatedAtLTE(filter.To)),
		))
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
		Order(orderByEffectiveDate(true)).
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
			Origin:              t.Origin,
			SourceWarehouseName: srcName,
			DestWarehouseName:   destName,
			LineCount:           len(t.Edges.Lines),
			ShippedAt:           t.ShippedAt,
			ReceivedAt:          t.ReceivedAt,
			TransferDate:        t.TransferDate,
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

// ReceiveTransfer transitions a transfer from in_transit to received. Each line credits its
// full drafted Quantity to the destination warehouse UNLESS req overrides it with what actually
// arrived (breakage/loss in transit, a miscount at dispatch) — omit req.Items entirely for the
// common full-receipt case; zero behavior change from before this override existed.
func (s *Service) ReceiveTransfer(ctx context.Context, tenantID, transferID uuid.UUID, req ReceiveTransferRequest) error {
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

	overrideByLineID := make(map[uuid.UUID]ReceiveLineInput, len(req.Items))
	for _, ri := range req.Items {
		overrideByLineID[ri.LineID] = ri
	}

	// Credit each line to the DESTINATION warehouse balance (stock has arrived) — the full
	// drafted quantity by default, or the caller's override when one was supplied for that line.
	// Persist ReceivedQuantity/VarianceReason on each line so the response, the outbox event,
	// and any later read of this transfer all agree on what actually landed.
	receivedLines := make([]*ent.StockTransferLine, 0, len(transfer.Edges.Lines))
	for _, line := range transfer.Edges.Lines {
		receivedQty := line.Quantity
		varianceReason := ""
		if ri, ok := overrideByLineID[line.ID]; ok {
			if ri.ReceivedQty < 0 {
				err = fmt.Errorf("transfers: received quantity for line %s cannot be negative", line.ID)
				return err
			}
			if ri.ReceivedQty > line.Quantity {
				err = fmt.Errorf("transfers: received quantity for line %s (%g) cannot exceed the shipped quantity (%g)", line.ID, ri.ReceivedQty, line.Quantity)
				return err
			}
			receivedQty = ri.ReceivedQty
			varianceReason = ri.VarianceReason
		}
		if err = s.adjustBalance(ctx, tx, tenantID, line.ItemID, transfer.DestinationWarehouseID, receivedQty); err != nil {
			return err
		}
		lineUpdate := tx.StockTransferLine.UpdateOne(line).SetReceivedQuantity(receivedQty)
		if varianceReason != "" {
			lineUpdate = lineUpdate.SetVarianceReason(varianceReason)
		}
		updatedLine, uerr := lineUpdate.Save(ctx)
		if uerr != nil {
			err = fmt.Errorf("transfers: record received quantity: %w", uerr)
			return err
		}
		receivedLines = append(receivedLines, updatedLine)
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
		"items":                    s.buildLineItems(receivedLines),
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

// generateTransferNumber generates a unique transfer number for a tenant, dated `at` — the
// transfer's own effective date when backdated/postdated (see resolveTransferDate), else
// time.Now() for a normal same-day transfer or an auto-recorded one (RecordCompletedTransfer).
// The atomic counter behind it (SequenceService.GenerateNumberAt) always advances in real call
// order regardless of `at`, so this never collides with another number — only the number's own
// printed date portion (when the tenant's sequence config includes one) reflects `at` instead of
// always today. Format: TRF-{date}-{seq} (tenant-configured) or the legacy TRF-{YYYYMMDD}-{seq}.
func (s *Service) generateTransferNumber(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, at time.Time) (string, error) {
	if s.seq != nil {
		if n, err := s.seq.GenerateNumberAt(ctx, tenantID, documents.DocTypeStockTransfer, at); err == nil && n != "" {
			return n, nil
		}
	}
	today := at.Format("20060102")
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
	name, sku, unit string
}

// unitAbbrev reads an item's unit-of-measure abbreviation off its eager-loaded Units edge (see
// validateTransferLines/itemDisplayMap's WithUnits()). Empty when the item has no unit assigned
// or the edge wasn't loaded, rather than panicking on the nil edge.
func unitAbbrev(it *ent.Item) string {
	if it.Edges.Units == nil {
		return ""
	}
	return it.Edges.Units.Abbreviation
}

// itemDisplayMap batch-loads name/sku/unit for a set of item IDs. Missing/unresolvable items are
// simply absent from the map (buildTransferResponse leaves their line's name/sku/unit blank
// rather than failing the whole response — an item transferred and later hard-deleted must never
// break the historical transfer record).
func (s *Service) itemDisplayMap(ctx context.Context, client *ent.Client, ids []uuid.UUID) map[uuid.UUID]itemDisplay {
	out := make(map[uuid.UUID]itemDisplay, len(ids))
	if len(ids) == 0 {
		return out
	}
	items, err := client.Item.Query().Where(item.IDIn(ids...)).WithUnits().All(ctx)
	if err != nil {
		s.log.Warn("transfers: load item display info failed", zap.Error(err))
		return out
	}
	for _, it := range items {
		out[it.ID] = itemDisplay{name: it.Name, sku: it.Sku, unit: unitAbbrev(it)}
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
		Origin:         transfer.Origin,
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
		TransferDate:    transfer.TransferDate,
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
			ItemUnit:  info.unit,
			VariantID: l.VariantID,
			LotID:     l.LotID,
			Quantity:  l.Quantity,
		}
		if l.ReceivedQuantity != nil {
			line.ReceivedQty = l.ReceivedQuantity
			line.VarianceReason = l.VarianceReason
		} else if received {
			// Legacy rows received before this field existed: fall back to the drafted quantity
			// (the only value that was ever credited before ReceivedQuantity was introduced).
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
		if l.ReceivedQuantity != nil {
			item["received_quantity"] = *l.ReceivedQuantity
		}
		if l.VarianceReason != "" {
			item["variance_reason"] = l.VarianceReason
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
