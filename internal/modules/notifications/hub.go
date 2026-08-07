// Package notifications provides a tenant-scoped WebSocket hub for real-time UI push —
// inventory-ui connects here to learn about stock changes the instant they happen (a POS sale
// consuming stock, a manual adjustment, a stock-take) instead of only finding out on a manual
// page refresh. Modeled directly on pos-api's internal/modules/notifications.Hub (same
// nhooyr.io/websocket transport) plus pos-api's internal/modules/printing.Hub's Redis pub/sub
// cross-pod relay (inventory-api's own catalog_changed-equivalent hub had none, which is a
// single-pod-only gap at >1 replica — this hub is built correctly multi-replica-safe from the
// start instead of repeating that gap).
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Message is the envelope pushed to notification WebSocket clients. Payloads are deliberately
// thin invalidation nudges (type + a few ids) — never a full row — so clients react by
// invalidating the matching query, not by trusting the push as authoritative data.
type Message struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// client is a single connected WebSocket session, scoped to a tenant.
type client struct {
	conn     *websocket.Conn
	tenantID uuid.UUID
	send     chan Message
}

// Hub manages active notification WebSocket connections, scoped to tenantID, with a Redis
// pub/sub relay so a broadcast triggered on one pod reaches clients connected to any pod.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
	log     *zap.Logger
	redis   *redis.Client
}

// NewHub creates a new notification hub.
func NewHub(log *zap.Logger) *Hub {
	return &Hub{
		clients: make(map[*client]struct{}),
		log:     log.Named("notif.hub"),
	}
}

// SetRedis wires the cross-pod relay client. Call before Start.
func (h *Hub) SetRedis(rdb *redis.Client) { h.redis = rdb }

const relayChannelPrefix = "invnotif:"

// Start subscribes to the cross-pod relay channel and re-broadcasts locally. Blocks until ctx is
// cancelled — run in a goroutine. A nil Redis client degrades to single-pod-only delivery (still
// correct with exactly one replica).
func (h *Hub) Start(ctx context.Context) {
	if h.redis == nil {
		h.log.Info("notif.hub: no Redis client — single-pod broadcast only")
		return
	}
	sub := h.redis.PSubscribe(ctx, relayChannelPrefix+"*")
	defer func() { _ = sub.Close() }()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case rmsg, ok := <-ch:
			if !ok {
				return
			}
			tenantID, ok := parseRelayChannel(rmsg.Channel)
			if !ok {
				continue
			}
			var msg Message
			if err := json.Unmarshal([]byte(rmsg.Payload), &msg); err != nil {
				continue
			}
			h.broadcastLocal(tenantID, msg)
		}
	}
}

func parseRelayChannel(channel string) (uuid.UUID, bool) {
	id, ok := strings.CutPrefix(channel, relayChannelPrefix)
	if !ok {
		return uuid.Nil, false
	}
	tenantID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, false
	}
	return tenantID, true
}

// BroadcastToTenant delivers a message to every active session for the tenant — locally, and (via
// Redis) on every other replica too. Never blocks the caller (typically a NATS consumer handler).
func (h *Hub) BroadcastToTenant(tenantID uuid.UUID, msg Message) {
	h.broadcastLocal(tenantID, msg)
	if h.redis == nil {
		return
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	channel := fmt.Sprintf("%s%s", relayChannelPrefix, tenantID)
	if err := h.redis.Publish(context.Background(), channel, payload).Err(); err != nil {
		h.log.Warn("notif.hub: redis publish failed", zap.Error(err), zap.String("channel", channel))
	}
}

func (h *Hub) broadcastLocal(tenantID uuid.UUID, msg Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.tenantID != tenantID {
			continue
		}
		select {
		case c.send <- msg:
		default:
			h.log.Warn("notif.hub: send buffer full, dropping message", zap.Stringer("tenant_id", tenantID))
		}
	}
}

// ServeWS upgrades the HTTP connection and blocks until the client disconnects.
func (h *Hub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	c := &client{conn: conn, tenantID: tenantID, send: make(chan Message, 16)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	defer func() {
		// Delete under the write lock BEFORE closing send: once removed, broadcastLocal (which
		// holds only an RLock) can no longer reach c, so this can never race a send on a closed
		// channel.
		h.mu.Lock()
		delete(h.clients, c)
		close(c.send)
		h.mu.Unlock()
	}()

	go func() {
		for msg := range c.send {
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(writeCtx, conn, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}()

	c.send <- Message{Type: "ping", Payload: map[string]any{"ts": time.Now().Unix()}}

	// Reader loop — keepalive; any inbound message is a client ping, answered with a pong. Exits
	// when the socket drops (or ctx cancels), which unregisters the connection via the defer.
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			break
		}
		select {
		case c.send <- Message{Type: "pong"}:
		default:
		}
	}
}
