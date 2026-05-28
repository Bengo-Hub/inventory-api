package tenant

import (
	"context"
	"fmt"
	"strings"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// inventoryAcceptedUseCases is the set of outlet use_cases whose outlets get a warehouse mirror.
// Logistics hubs and weighbridge stations don't hold inventory stock.
var inventoryAcceptedUseCases = map[string]bool{
	"hospitality":  true,
	"retail":       true,
	"quick_service": true,
	"pharmacy":     true,
	"services":     true,
	"warehouse":    true,
}

const authStream = "auth"

// BranchSubscriber syncs auth.outlet.* events from auth-api into inventory
// warehouses, keeping the per-outlet warehouse in sync automatically.
type BranchSubscriber struct {
	orm    *ent.Client
	logger *zap.Logger
}

func NewBranchSubscriber(orm *ent.Client, logger *zap.Logger) *BranchSubscriber {
	return &BranchSubscriber{
		orm:    orm,
		logger: logger.Named("tenant.branch_subscriber"),
	}
}

// Start subscribes to auth.outlet.* via JetStream durable consumers.
func (s *BranchSubscriber) Start(nc *nats.Conn) error {
	if nc == nil {
		s.logger.Warn("NATS not available, skipping outlet event subscriptions")
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("outlet events: jetstream init: %w", err)
	}

	// Ensure the auth stream exists (guard against startup race with auth-api).
	if _, err := js.StreamInfo(authStream); err != nil {
		if _, addErr := js.AddStream(&nats.StreamConfig{
			Name:      authStream,
			Subjects:  []string{"auth.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); addErr != nil && addErr != nats.ErrStreamNameAlreadyInUse {
			s.logger.Warn("outlet events: ensure auth stream failed", zap.Error(addErr))
		}
	}

	type sub struct {
		subject string
		durable string
		handler func(context.Context, *sharedevents.Event) error
	}
	subs := []sub{
		{"auth.outlet.created", "inv-auth-outlet-created", s.handleUpsert},
		{"auth.outlet.updated", "inv-auth-outlet-updated", s.handleUpsert},
		{"auth.outlet.archived", "inv-auth-outlet-archived", s.handleArchive},
	}

	for _, cfg := range subs {
		cfg := cfg
		if _, subErr := js.Subscribe(cfg.subject, func(msg *nats.Msg) {
			evt, err := sharedevents.FromJSON(msg.Data)
			if err != nil {
				s.logger.Error("failed to unmarshal outlet event",
					zap.String("subject", cfg.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			if err := cfg.handler(context.Background(), evt); err != nil {
				s.logger.Error("failed to handle outlet event",
					zap.String("subject", cfg.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
			nats.Durable(cfg.durable),
			nats.AckExplicit(),
			nats.AckWait(30*time.Second),
			nats.MaxDeliver(5),
			nats.DeliverAll(),
		); subErr != nil {
			s.logger.Warn("outlet events: subscribe failed",
				zap.String("subject", cfg.subject), zap.Error(subErr))
		}
	}

	s.logger.Info("outlet event subscriptions active",
		zap.String("subjects", "auth.outlet.created, auth.outlet.updated, auth.outlet.archived"))
	return nil
}

// handleUpsert creates or updates an inventory warehouse to mirror the outlet.
func (s *BranchSubscriber) handleUpsert(ctx context.Context, evt *sharedevents.Event) error {
	outletIDStr, _ := evt.Payload["outlet_id"].(string)
	code, _ := evt.Payload["code"].(string)
	name, _ := evt.Payload["name"].(string)
	useCase, _ := evt.Payload["use_case"].(string)
	isHQ, _ := evt.Payload["is_hq"].(bool)
	status, _ := evt.Payload["status"].(string)

	// Logistics hubs, weighbridge stations, and enforcement checkpoints don't hold stock.
	// Only create warehouses for outlets that actually store inventory.
	if useCase != "" && !inventoryAcceptedUseCases[useCase] {
		s.logger.Info("skipping outlet: use_case not applicable to inventory warehouses",
			zap.String("outlet_id", outletIDStr),
			zap.String("use_case", useCase))
		return nil
	}

	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return fmt.Errorf("invalid outlet_id %q: %w", outletIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in outlet event")
	}

	whCode := strings.ToUpper(code)
	if whCode == "" {
		whCode = slugify(name)
	}

	existing, err := s.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(evt.TenantID), entwarehouse.OutletID(outletID)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query warehouse: %w", err)
	}

	if ent.IsNotFound(err) {
		if _, err := s.orm.Warehouse.Create().
			SetTenantID(evt.TenantID).
			SetOutletID(outletID).
			SetName(name).
			SetCode(whCode).
			SetIsDefault(isHQ).
			SetIsActive(status != "archived").
			Save(ctx); err != nil {
			return fmt.Errorf("create warehouse mirror: %w", err)
		}
		s.logger.Info("warehouse created from auth.outlet event",
			zap.String("outlet_id", outletIDStr),
			zap.String("code", whCode),
			zap.String("use_case", useCase))
		return nil
	}

	if _, err := s.orm.Warehouse.UpdateOne(existing).
		SetName(name).
		SetIsDefault(isHQ).
		SetIsActive(status != "archived").
		Save(ctx); err != nil {
		return fmt.Errorf("update warehouse mirror: %w", err)
	}
	s.logger.Info("warehouse updated from auth.outlet event",
		zap.String("outlet_id", outletIDStr),
		zap.String("code", existing.Code))
	return nil
}

// handleArchive deactivates the warehouse when the outlet is archived.
// If the outlet was never mirrored (filtered by use_case), this is a no-op.
func (s *BranchSubscriber) handleArchive(ctx context.Context, evt *sharedevents.Event) error {
	outletIDStr, _ := evt.Payload["outlet_id"].(string)
	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return fmt.Errorf("invalid outlet_id %q: %w", outletIDStr, err)
	}

	n, err := s.orm.Warehouse.Update().
		Where(entwarehouse.TenantID(evt.TenantID), entwarehouse.OutletID(outletID)).
		SetIsActive(false).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("deactivate warehouse: %w", err)
	}
	if n > 0 {
		s.logger.Info("warehouse deactivated from auth.outlet.archived",
			zap.String("outlet_id", outletIDStr))
	}
	return nil // n==0 means outlet was never mirrored — safe to ignore
}

// slugify converts a name to an upper-case alphanumeric code (max 8 chars).
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 8 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return uuid.New().String()[:8]
	}
	return b.String()
}
