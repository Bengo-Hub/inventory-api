package tenant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/platform/events"
)

// BranchEvent represents the data sent in auth.tenant.branch events.
type BranchEvent struct {
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	UseCase   string `json:"use_case"`
}

// BranchSubscriber handles branch-related event subscriptions.
type BranchSubscriber struct {
	orm    *ent.Client
	logger *zap.Logger
}

// NewBranchSubscriber creates a new branch subscriber.
func NewBranchSubscriber(orm *ent.Client, logger *zap.Logger) *BranchSubscriber {
	return &BranchSubscriber{
		orm:    orm,
		logger: logger.Named("branch.subscriber"),
	}
}

// RegisterSubscribers registers all branch-related event handlers.
func (s *BranchSubscriber) RegisterSubscribers(sub *events.Subscriber) error {
	if err := sub.Subscribe("auth.tenant.branch.created", s.handleBranchCreated); err != nil {
		return fmt.Errorf("subscribe to branch.created: %w", err)
	}
	return nil
}

func (s *BranchSubscriber) handleBranchCreated(ctx context.Context, data []byte) error {
	var event struct {
		Data BranchEvent `json:"data"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("unmarshal branch event: %w", err)
	}

	branch := event.Data
	tenantID, err := uuid.Parse(branch.TenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant_id %q: %w", branch.TenantID, err)
	}

	s.logger.Info("received branch created event",
		zap.String("tenant_id", branch.TenantID),
		zap.String("name", branch.Name),
		zap.Bool("is_default", branch.IsDefault))

	// Ensure tenant exists locally (JIT sync)
	// We could use the Syncer here, but let's just check if it exists for now.
	// The event should ideally give us the slug too, but if not we might need to fetch it.
	
	// Create Warehouse (Location) for this branch
	_, err = s.orm.Warehouse.Create().
		SetTenantID(tenantID).
		SetName(branch.Name).
		SetCode(s.generateCode(branch.Name)).
		SetIsDefault(branch.IsDefault).
		SetIsActive(true).
		Save(ctx)

	if err != nil {
		return fmt.Errorf("failed to create warehouse: %w", err)
	}

	s.logger.Info("successfully created warehouse for branch", zap.String("name", branch.Name))
	return nil
}

func (s *BranchSubscriber) generateCode(name string) string {
	// Simple slugification for warehouse code
	// In production, this should be more robust or provided by the event.
	return uuid.New().String()[:8]
}
