package items

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/stockclearance"
)

// AgingStockRow is one candidate line on the Aging Stock report: an item whose oldest active
// lot is older than the tenant's configured threshold, with no clearance already running.
type AgingStockRow struct {
	ItemID           uuid.UUID `json:"item_id"`
	SKU              string    `json:"sku"`
	Name             string    `json:"name"`
	CurrentPrice     float64   `json:"current_price"`
	OldestReceivedAt time.Time `json:"oldest_received_at"`
	AgedQuantity     float64   `json:"aged_quantity"`
	AgeDays          int       `json:"age_days"`
}

// AgingStockReport lists items whose oldest active lot (received_at, quantity>0, status=active)
// predates now-thresholdDays, excluding items already under an active StockClearance (so the
// report only surfaces actionable candidates, not stock already being cleared). Best-effort:
// items with no cost-layer/lot data at all (e.g. a tenant not using lot tracking) never appear —
// there is nothing to date the stock by.
func (s *Service) AgingStockReport(ctx context.Context, tenantID uuid.UUID, thresholdDays int) ([]AgingStockRow, error) {
	if thresholdDays <= 0 {
		thresholdDays = 90
	}
	cutoff := time.Now().AddDate(0, 0, -thresholdDays)

	lots, err := s.client.InventoryLot.Query().
		Where(
			entlot.TenantID(tenantID),
			entlot.StatusEQ(entlot.StatusActive),
			entlot.QuantityGT(0),
			entlot.ReceivedAtLT(cutoff),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query aging lots: %w", err)
	}
	if len(lots) == 0 {
		return nil, nil
	}

	type agg struct {
		oldest time.Time
		qty    float64
	}
	byItem := make(map[uuid.UUID]*agg, len(lots))
	itemIDs := make([]uuid.UUID, 0, len(lots))
	for _, l := range lots {
		if l.ReceivedAt == nil {
			continue // ReceivedAtLT already excludes these at the SQL level; defensive only
		}
		a, ok := byItem[l.ItemID]
		if !ok {
			a = &agg{oldest: *l.ReceivedAt}
			byItem[l.ItemID] = a
			itemIDs = append(itemIDs, l.ItemID)
		}
		if l.ReceivedAt.Before(a.oldest) {
			a.oldest = *l.ReceivedAt
		}
		a.qty += l.Quantity
	}

	// Exclude items already under an active clearance.
	activeClearances, err := s.client.StockClearance.Query().
		Where(stockclearance.TenantID(tenantID), stockclearance.ItemIDIn(itemIDs...), stockclearance.StatusEQ(stockclearance.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query active clearances: %w", err)
	}
	alreadyClearing := make(map[uuid.UUID]bool, len(activeClearances))
	for _, c := range activeClearances {
		alreadyClearing[c.ItemID] = true
	}

	items, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.IDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: query aging items: %w", err)
	}

	now := time.Now()
	rows := make([]AgingStockRow, 0, len(items))
	for _, it := range items {
		if alreadyClearing[it.ID] {
			continue
		}
		a := byItem[it.ID]
		if a == nil {
			continue
		}
		price := 0.0
		if it.MaxSellingPrice != nil {
			price = *it.MaxSellingPrice
		}
		rows = append(rows, AgingStockRow{
			ItemID:           it.ID,
			SKU:              it.Sku,
			Name:             it.Name,
			CurrentPrice:     price,
			OldestReceivedAt: a.oldest,
			AgedQuantity:     a.qty,
			AgeDays:          int(now.Sub(a.oldest).Hours() / 24),
		})
	}
	return rows, nil
}

// StartClearance creates (or replaces) the active markdown for an item: while active, price
// resolution (effectivePrice) serves markdownPrice instead of the normal recipe/tier price.
// referenceBefore should be the item's current oldest-active-lot received_at (from
// AgingStockReport) — the clearance auto-ends once no active lot older than that remains with
// quantity>0. endsAt is optional; nil means purely depletion-driven (no time limit).
func (s *Service) StartClearance(ctx context.Context, tenantID, itemID uuid.UUID, markdownPrice float64, referenceBefore time.Time, endsAt *time.Time, actorID uuid.UUID, notes string) (*ent.StockClearance, error) {
	if markdownPrice <= 0 {
		return nil, fmt.Errorf("items: markdown price must be positive")
	}
	if _, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.ID(itemID)).Only(ctx); err != nil {
		return nil, fmt.Errorf("items: item %s: %w", itemID, err)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Supersede any existing active clearance for this item (cancel it) before starting the new
	// one — the partial unique index only allows one active row per item, and a plain re-insert
	// would otherwise hit a constraint violation if staff re-runs "Start Clearance" with adjusted
	// terms.
	now := time.Now()
	if _, cErr := tx.StockClearance.Update().
		Where(stockclearance.TenantID(tenantID), stockclearance.ItemID(itemID), stockclearance.StatusEQ(stockclearance.StatusActive)).
		SetStatus(stockclearance.StatusCancelled).
		SetEndedAt(now).
		Save(ctx); cErr != nil {
		err = fmt.Errorf("items: supersede existing clearance: %w", cErr)
		return nil, err
	}

	create := tx.StockClearance.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetMarkdownPrice(markdownPrice).
		SetReferenceBefore(referenceBefore).
		SetStartsAt(now)
	if endsAt != nil {
		create = create.SetEndsAt(*endsAt)
	}
	if actorID != uuid.Nil {
		create = create.SetCreatedBy(actorID)
	}
	if notes != "" {
		create = create.SetNotes(notes)
	}
	saved, cErr := create.Save(ctx)
	if cErr != nil {
		err = fmt.Errorf("items: create clearance: %w", cErr)
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit clearance: %w", err)
	}

	if s.auditSvc != nil {
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      "item.clearance_started",
			EntityType:  "item",
			EntityID:    itemID.String(),
			Reason:      notes,
			After:       map[string]any{"markdown_price": markdownPrice, "reference_before": referenceBefore, "ends_at": endsAt},
		})
	}
	return saved, nil
}

// CancelClearance manually ends an item's active clearance early (e.g. staff changed their
// mind, or found a pricing mistake). No-op if none is active.
func (s *Service) CancelClearance(ctx context.Context, tenantID, itemID, actorID uuid.UUID) error {
	n, err := s.client.StockClearance.Update().
		Where(stockclearance.TenantID(tenantID), stockclearance.ItemID(itemID), stockclearance.StatusEQ(stockclearance.StatusActive)).
		SetStatus(stockclearance.StatusCancelled).
		SetEndedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("items: cancel clearance: %w", err)
	}
	if n > 0 && s.auditSvc != nil {
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      "item.clearance_cancelled",
			EntityType:  "item",
			EntityID:    itemID.String(),
		})
	}
	return nil
}

// PromoteExpiredClearances flips ACTIVE clearances to expired/depleted once their end condition
// is met: ends_at has passed, or no active lot older than reference_before remains with
// quantity>0 — whichever comes first. Same lazy-check-on-resolve idiom as
// PromotePendingPriceChanges: called from the price-resolution paths, best-effort, never blocks
// price resolution on failure.
func (s *Service) PromoteExpiredClearances(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) {
	if len(itemIDs) == 0 {
		return
	}
	active, err := s.client.StockClearance.Query().
		Where(stockclearance.TenantID(tenantID), stockclearance.ItemIDIn(itemIDs...), stockclearance.StatusEQ(stockclearance.StatusActive)).
		All(ctx)
	if err != nil || len(active) == 0 {
		return
	}
	now := time.Now()
	for _, c := range active {
		newStatus := stockclearance.Status("")
		switch {
		case c.EndsAt != nil && !now.Before(*c.EndsAt):
			newStatus = stockclearance.StatusExpired
		default:
			stillOldStock, sErr := s.client.InventoryLot.Query().
				Where(entlot.TenantID(tenantID), entlot.ItemID(c.ItemID), entlot.StatusEQ(entlot.StatusActive), entlot.QuantityGT(0), entlot.ReceivedAtLT(c.ReferenceBefore)).
				Exist(ctx)
			if sErr != nil || stillOldStock {
				continue // old stock remains (or the check failed) — clearance stays active
			}
			newStatus = stockclearance.StatusDepleted
		}
		// Compare-and-swap on (id, status=active): only the caller that wins this flip actually
		// ends the clearance, so a concurrent promotion attempt can't double-process.
		_, _ = s.client.StockClearance.Update().
			Where(stockclearance.ID(c.ID), stockclearance.StatusEQ(stockclearance.StatusActive)).
			SetStatus(newStatus).
			SetEndedAt(now).
			Save(ctx)
	}
}

// activeClearancePrices maps item_id → markdown price for every ACTIVE clearance among itemIDs.
// Callers should invoke PromoteExpiredClearances first so an expired/depleted clearance never
// shows up here.
func (s *Service) activeClearancePrices(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	rows, err := s.client.StockClearance.Query().
		Where(stockclearance.TenantID(tenantID), stockclearance.ItemIDIn(itemIDs...), stockclearance.StatusEQ(stockclearance.StatusActive)).
		All(ctx)
	if err != nil {
		return out
	}
	for _, c := range rows {
		if c.MarkdownPrice > 0 {
			out[c.ItemID] = c.MarkdownPrice
		}
	}
	return out
}
