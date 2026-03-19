package events

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Event represents a basic event structure.
type Event struct {
	ID        string         `json:"id"`
	Source    string         `json:"source"`
	Type      string         `json:"type"`
	Timestamp int64          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// Subscriber handles subscribing to events from NATS.
type Subscriber struct {
	conn   *nats.Conn
	logger *zap.Logger
	subs   []*nats.Subscription
}

// NewSubscriber creates a new event subscriber.
func NewSubscriber(conn *nats.Conn, logger *zap.Logger) *Subscriber {
	return &Subscriber{
		conn:   conn,
		logger: logger.Named("events.subscriber"),
		subs:   make([]*nats.Subscription, 0),
	}
}

// EventHandler is a function that handles an event.
type EventHandler func(ctx context.Context, data []byte) error

// Subscribe subscribes to a subject with the given handler.
func (s *Subscriber) Subscribe(subject string, handler EventHandler) error {
	if s.conn == nil {
		s.logger.Warn("NATS connection not available, skipping subscription",
			zap.String("subject", subject))
		return nil
	}

	sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
		ctx := context.Background()
		if err := handler(ctx, msg.Data); err != nil {
			s.logger.Error("failed to handle event",
				zap.Error(err),
				zap.String("subject", subject))
		}
	})

	if err != nil {
		return err
	}

	s.subs = append(s.subs, sub)
	s.logger.Info("subscribed to subject", zap.String("subject", subject))

	return nil
}

// Unsubscribe unsubscribes from all subscriptions.
func (s *Subscriber) Unsubscribe() error {
	for _, sub := range s.subs {
		if err := sub.Unsubscribe(); err != nil {
			s.logger.Warn("failed to unsubscribe",
				zap.Error(err),
				zap.String("subject", sub.Subject))
		}
	}
	s.subs = nil
	return nil
}
