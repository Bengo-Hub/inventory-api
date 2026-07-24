// Package expiry implements the DAWA (pharmacy) expiry-alert job: consuming the
// TenantInventoryConfig.expiry_warning_days/enable_expiry_notifications fields that were,
// until this phase, stored and exposed via settings but never read by anything.
package expiry

import (
	"context"
	"encoding/json"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/expiryalertlog"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	enttenantcfg "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

// criticalThresholdDays is the hardcoded escalation tier — regardless of a tenant's configured
// expiry_warning_days, anything within this many days is always additionally flagged "critical".
const criticalThresholdDays = 7

// Service scans every tenant with expiry notifications enabled for lots approaching expiry.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService builds the expiry-alert service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{client: client, log: log.Named("expiry.Service")}
}

// RunExpiryCheck scans every tenant with enable_expiry_notifications=true, finds active
// InventoryLot rows expiring within that tenant's configured expiry_warning_days, and emits
// one inventory.lot.expiry_warning event per (lot, tier) not already alerted. Returns the
// number of NEW alerts emitted (already-alerted lots are silently skipped, not an error).
func (s *Service) RunExpiryCheck(ctx context.Context) (int, error) {
	configs, err := s.client.TenantInventoryConfig.Query().
		Where(enttenantcfg.EnableExpiryNotifications(true)).
		All(ctx)
	if err != nil {
		return 0, err
	}

	alerted := 0
	now := time.Now()
	for _, cfg := range configs {
		warningDays := cfg.ExpiryWarningDays
		if warningDays <= 0 {
			continue
		}
		cutoff := now.AddDate(0, 0, warningDays)
		lots, err := s.client.InventoryLot.Query().
			Where(
				entlot.TenantID(cfg.TenantID),
				entlot.StatusEQ(entlot.StatusActive),
				entlot.QuantityGT(0),
				entlot.ExpiryDateNotNil(),
				entlot.ExpiryDateLTE(cutoff),
			).
			All(ctx)
		if err != nil {
			s.log.Warn("expiry check: query lots failed", zap.String("tenant_id", cfg.TenantID.String()), zap.Error(err))
			continue
		}
		for _, lot := range lots {
			tier := expiryalertlog.TierWarning
			if lot.ExpiryDate != nil && lot.ExpiryDate.Before(now.AddDate(0, 0, criticalThresholdDays)) {
				tier = expiryalertlog.TierCritical
			}
			if s.alertOnce(ctx, cfg.TenantID, lot, tier) {
				alerted++
			}
		}
	}
	return alerted, nil
}

// alertOnce writes the ExpiryAlertLog row (idempotency guard) and, only if that insert wins
// (i.e. this lot/tier hasn't been alerted before), publishes the outbox event. Both happen in
// one transaction so a crash between them can't either double-alert or silently drop the event.
func (s *Service) alertOnce(ctx context.Context, tenantID uuid.UUID, lot *ent.InventoryLot, tier expiryalertlog.Tier) bool {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		s.log.Warn("expiry check: begin tx failed", zap.Error(err))
		return false
	}

	_, err = tx.ExpiryAlertLog.Create().
		SetTenantID(tenantID).
		SetLotID(lot.ID).
		SetTier(tier).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return false // already alerted for this lot/tier — expected, not a failure
		}
		s.log.Warn("expiry check: write alert log failed", zap.String("lot_id", lot.ID.String()), zap.Error(err))
		return false
	}

	payload := map[string]any{
		"lot_id":       lot.ID.String(),
		"item_id":      lot.ItemID.String(),
		"lot_number":   lot.LotNumber,
		"warehouse_id": lot.WarehouseID.String(),
		"tier":         tier,
	}
	if lot.ExpiryDate != nil {
		payload["expiry_date"] = lot.ExpiryDate.Format(time.RFC3339)
	}
	evt := eventslib.NewEvent("lot.expiry_warning", "inventory", lot.ID, tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		s.log.Warn("expiry check: marshal event failed", zap.Error(err))
		_ = tx.Rollback()
		return false
	}
	if _, err := tx.OutboxEvent.Create().
		SetID(evt.ID).
		SetTenantID(tenantID).
		SetAggregateType("inventory").
		SetAggregateID(lot.ID.String()).
		SetEventType("lot.expiry_warning").
		SetPayload(json.RawMessage(data)).
		Save(ctx); err != nil {
		s.log.Warn("expiry check: write outbox event failed", zap.Error(err))
		_ = tx.Rollback()
		return false
	}

	if err := tx.Commit(); err != nil {
		s.log.Warn("expiry check: commit failed", zap.Error(err))
		return false
	}
	return true
}
