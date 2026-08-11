package handlers

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	notifmod "github.com/bengobox/inventory-service/internal/modules/notifications"
)

// NotificationsStreamHandler serves the tenant-scoped real-time push WebSocket. inventory-ui
// connects here so a stock change (POS sale consumption, manual adjustment, stock-take) shows up
// live instead of only after a manual page refresh.
type NotificationsStreamHandler struct {
	log *zap.Logger
	hub *notifmod.Hub
}

// NewNotificationsStreamHandler wires the hub used both here and by the stock-change consumer.
func NewNotificationsStreamHandler(log *zap.Logger, hub *notifmod.Hub) *NotificationsStreamHandler {
	return &NotificationsStreamHandler{log: log.Named("notifications.stream"), hub: hub}
}

// StreamNotifications handles GET /{tenant}/inventory/notifications/stream.
func (h *NotificationsStreamHandler) StreamNotifications(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		return
	}
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_tenant_id", "invalid tenant_id")
		return
	}

	conn, wsErr := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if wsErr != nil {
		h.log.Warn("websocket upgrade failed", zap.Error(wsErr))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// nil (unrestricted) unless an outlet is resolved for this session — an HQ user with no outlet
	// drill-down keeps seeing every outlet's pushes, matching the pre-outlet-scoping behavior; a
	// branch-restricted or drilled-down session only sees its own. A browser WebSocket handshake
	// can't carry the usual X-Outlet-ID header, so ?outlet_id= is the primary source here; the
	// header (set by OutletContext from a non-HQ user's JWT claim, no override needed) still wins
	// when present since it reflects a hard restriction rather than a UI choice.
	var outletID *uuid.UUID
	if v := invmiddleware.GetOutletID(r.Context()); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			outletID = &id
		}
	} else if v := r.URL.Query().Get("outlet_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			outletID = &id
		}
	}

	h.hub.ServeWS(r.Context(), conn, tenantID, outletID)
}
