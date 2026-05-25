package consumers

import (
	"context"
	"fmt"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

const authEventsStream = "auth"

// AuthEventsConsumer consumes auth-service user events for proactive user sync.
type AuthEventsConsumer struct {
	log     *zap.Logger
	rbacSvc *rbac.Service
}

func NewAuthEventsConsumer(log *zap.Logger, rbacSvc *rbac.Service) *AuthEventsConsumer {
	return &AuthEventsConsumer{
		log:     log.Named("consumers.auth_events"),
		rbacSvc: rbacSvc,
	}
}

// Start subscribes to auth.user.* via JetStream durable consumers.
func (c *AuthEventsConsumer) Start(ctx context.Context, nc *nats.Conn) error {
	if nc == nil {
		c.log.Warn("NATS not available, skipping auth user event subscriptions")
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("auth events: jetstream init: %w", err)
	}

	// Ensure auth stream exists (guard against startup race with auth-api).
	if _, err := js.StreamInfo(authEventsStream); err != nil {
		if _, addErr := js.AddStream(&nats.StreamConfig{
			Name:      authEventsStream,
			Subjects:  []string{"auth.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); addErr != nil && addErr != nats.ErrStreamNameAlreadyInUse {
			c.log.Warn("auth events: ensure auth stream failed", zap.Error(addErr))
		}
	}

	type sub struct {
		subject string
		durable string
		handler func(context.Context, *sharedevents.Event) error
	}
	subs := []sub{
		{"auth.user.created", "inv-auth-user-created", c.handleUserCreated},
		{"auth.user.updated", "inv-auth-user-updated", c.handleUserUpdated},
	}

	for _, s := range subs {
		s := s
		if _, subErr := js.Subscribe(s.subject, func(msg *nats.Msg) {
			evt, err := sharedevents.FromJSON(msg.Data)
			if err != nil {
				c.log.Error("failed to unmarshal auth user event",
					zap.String("subject", s.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			if err := s.handler(context.Background(), evt); err != nil {
				c.log.Error("failed to handle auth user event",
					zap.String("subject", s.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
			nats.Durable(s.durable),
			nats.AckExplicit(),
			nats.AckWait(30*time.Second),
			nats.MaxDeliver(5),
			nats.DeliverAll(),
		); subErr != nil {
			c.log.Warn("auth events: subscribe failed",
				zap.String("subject", s.subject), zap.Error(subErr))
		}
	}

	c.log.Info("auth user event subscriptions active",
		zap.String("subjects", "auth.user.created, auth.user.updated"))
	return nil
}

func (c *AuthEventsConsumer) handleUserCreated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.created event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, evt.TenantID, userID, email); err != nil {
		return fmt.Errorf("sync user from auth.user.created: %w", err)
	}

	c.log.Info("user synced from auth.user.created",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", evt.TenantID.String()))
	return nil
}

func (c *AuthEventsConsumer) handleUserUpdated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.updated event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, evt.TenantID, userID, email); err != nil {
		return fmt.Errorf("sync user from auth.user.updated: %w", err)
	}

	c.log.Info("user synced from auth.user.updated",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", evt.TenantID.String()))
	return nil
}
