// Package notifications provides a tenant-scoped WebSocket hub for real-time UI push:
// inventory-ui connects here to learn about stock changes the instant they happen (a POS sale
// consuming stock, a manual adjustment, a stock-take) instead of only on a manual refresh.
package notifications

import (
	"context"
	"encoding/json"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"
)

// Message is the envelope pushed to notification WebSocket clients. Payloads are deliberately
// thin invalidation nudges (type + a few ids), never a full row, so clients react by
// invalidating the matching query, not by trusting the push as authoritative data.
type Message struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// Hub fans stock/catalog nudges out to inventory-ui sessions on every replica through the shared
// events.FanoutHub (the previous Redis relay had no origin check, so the publishing replica
// delivered every message twice). A session opened for one outlet gets that outlet's messages
// plus tenant-wide ones; a session with no outlet (the "All Outlets" view) gets everything.
type Hub struct {
	fan *eventslib.FanoutHub
	log *zap.Logger
}

const relayTopic = "inventory-notifications"

// NewHub creates a hub with local-only delivery until SetRelay wires the cross-replica relay.
func NewHub(log *zap.Logger) *Hub {
	fan, _ := eventslib.NewFanoutHub(nil, relayTopic, 16)
	return &Hub{fan: fan, log: log.Named("notif.hub")}
}

// SetRelay wires the cross-replica relay. Call once at startup, before serving traffic.
func (h *Hub) SetRelay(b *eventslib.Broadcaster) {
	fan, err := eventslib.NewFanoutHub(b, relayTopic, 16)
	if err != nil {
		h.log.Warn("notif.hub: relay subscribe failed, single-pod delivery only", zap.Error(err))
	}
	h.fan = fan
}

// BroadcastToTenant nudges every session of the tenant.
func (h *Hub) BroadcastToTenant(tenantID uuid.UUID, msg Message) {
	h.publish(tenantID, "", msg)
}

// BroadcastToOutlet nudges the outlet's sessions and the tenant's all-outlet sessions; a nil
// outlet means tenant-wide.
func (h *Hub) BroadcastToOutlet(tenantID uuid.UUID, outletID *uuid.UUID, msg Message) {
	scope := ""
	if outletID != nil {
		scope = "outlet:" + outletID.String()
	}
	h.publish(tenantID, scope, msg)
}

func (h *Hub) publish(tenantID uuid.UUID, scope string, msg Message) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.fan.Publish(tenantID.String(), scope, b)
}

// ServeWS serves one upgraded connection until it disconnects.
func (h *Hub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID, outletID *uuid.UUID) {
	scope := eventslib.WildcardScope
	if outletID != nil {
		scope = "outlet:" + outletID.String()
	}
	sub := h.fan.Subscribe(tenantID.String(), scope)
	defer h.fan.Unsubscribe(sub)
	hello, _ := json.Marshal(Message{Type: "ping"})
	eventslib.Pump(ctx, eventslib.Socket{
		Read:  func(ctx context.Context) ([]byte, error) { _, b, err := conn.Read(ctx); return b, err },
		Write: func(ctx context.Context, b []byte) error { return conn.Write(ctx, websocket.MessageText, b) },
		Ping:  conn.Ping,
	}, sub, hello, nil)
}
