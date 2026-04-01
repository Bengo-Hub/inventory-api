package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

const (
	authEventsDurableCreated = "inventory-service-auth-user-created"
	authEventsDurableUpdated = "inventory-service-auth-user-updated"
	authEventsAckWait        = 30 * time.Second
	authEventsMaxDeliver     = 5
)

// authUserEvent represents the shared-events envelope for auth user events.
// Auth-api publishes via outbox using shared-events format:
//
//	{ "event_type": "created", "aggregate_type": "auth.user", "tenant_id": "...", "payload": {...} }
type authUserEvent struct {
	EventType     string                 `json:"event_type"`
	AggregateType string                 `json:"aggregate_type"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	Payload       map[string]interface{} `json:"payload"`
}

// AuthEventsConsumer consumes auth-service user events for proactive user sync.
type AuthEventsConsumer struct {
	log     *zap.Logger
	rbacSvc *rbac.Service
}

// NewAuthEventsConsumer creates a new auth events consumer.
func NewAuthEventsConsumer(log *zap.Logger, rbacSvc *rbac.Service) *AuthEventsConsumer {
	return &AuthEventsConsumer{
		log:     log.Named("consumers.auth_events"),
		rbacSvc: rbacSvc,
	}
}

// Start begins listening for auth user events via NATS.
// Auth events are published on plain NATS subjects (auth.user.created, auth.user.updated)
// by the auth-api outbox publisher.
func (c *AuthEventsConsumer) Start(ctx context.Context, nc *nats.Conn) error {
	if nc == nil {
		c.log.Warn("NATS connection not available, skipping auth event subscriptions")
		return nil
	}

	// Subscribe to auth.user.created
	_, err := nc.Subscribe("auth.user.created", func(msg *nats.Msg) {
		var evt authUserEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			c.log.Error("failed to unmarshal auth.user.created event", zap.Error(err))
			return
		}

		if err := c.handleUserCreated(ctx, &evt); err != nil {
			c.log.Error("failed to handle auth.user.created event", zap.Error(err))
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("subscribe to auth.user.created: %w", err)
	}

	// Subscribe to auth.user.updated
	_, err = nc.Subscribe("auth.user.updated", func(msg *nats.Msg) {
		var evt authUserEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			c.log.Error("failed to unmarshal auth.user.updated event", zap.Error(err))
			return
		}

		if err := c.handleUserUpdated(ctx, &evt); err != nil {
			c.log.Error("failed to handle auth.user.updated event", zap.Error(err))
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("subscribe to auth.user.updated: %w", err)
	}

	c.log.Info("auth event subscriptions active",
		zap.Strings("subjects", []string{"auth.user.created", "auth.user.updated"}))
	return nil
}

func (c *AuthEventsConsumer) handleUserCreated(ctx context.Context, evt *authUserEvent) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}

	tenantID := evt.TenantID
	if tenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.created event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, tenantID, userID, email); err != nil {
		return fmt.Errorf("sync user from auth.user.created: %w", err)
	}

	c.log.Info("user synced from auth.user.created event",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", tenantID.String()),
		zap.String("email", email))
	return nil
}

func (c *AuthEventsConsumer) handleUserUpdated(ctx context.Context, evt *authUserEvent) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}

	tenantID := evt.TenantID
	if tenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.updated event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, tenantID, userID, email); err != nil {
		return fmt.Errorf("sync user from auth.user.updated: %w", err)
	}

	c.log.Info("user synced from auth.user.updated event",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", tenantID.String()),
		zap.String("email", email))
	return nil
}
