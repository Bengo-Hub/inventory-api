package consumers

import (
	"context"
	"fmt"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvuser "github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	entuseroutlet "github.com/bengobox/inventory-service/internal/ent/useroutlet"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

const authEventsStream = "auth"

// AuthEventsConsumer consumes auth-service user events for proactive user sync.
//
// Besides syncing the InventoryUser/RBAC shadow, it projects each user's outlet
// assignments (carried on the auth.user event) into the local UserOutlet table.
// UserOutlet is what /inventory/my-outlets uses to scope non-HQ users to the
// outlets they may log in to — it is the inventory-side mirror of the outlet
// memberships owned by auth-service.
type AuthEventsConsumer struct {
	log     *zap.Logger
	rbacSvc *rbac.Service
	orm     *ent.Client
	cache   *sharedcache.Aside
}

func NewAuthEventsConsumer(log *zap.Logger, rbacSvc *rbac.Service, orm *ent.Client, cache *sharedcache.Aside) *AuthEventsConsumer {
	return &AuthEventsConsumer{
		log:     log.Named("consumers.auth_events"),
		rbacSvc: rbacSvc,
		orm:     orm,
		cache:   cache,
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
		// Terminal/PIN provisioning: store the bcrypt PIN hash so /inventory/auth/pin/* works.
		{"auth.user.pin_set", "inv-auth-user-pin-set", c.handlePinSet},
		// Branding cache invalidation: tenant edits in auth must drop the cached tenant:{slug}
		// entry, otherwise document generation keeps serving stale company name/address/KRA PIN
		// for up to the 6h TTL.
		{"auth.tenant.updated", "inv-auth-tenant-updated", c.handleTenantChanged},
		{"auth.tenant.created", "inv-auth-tenant-created", c.handleTenantChanged},
	}

	for _, s := range subs {
		s := s
		sharedevents.SubscribeQueueWithRebind(c.log, js, "auth", s.subject, s.durable, func(msg *nats.Msg) {
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
		)
	}

	c.log.Info("auth event subscriptions active",
		zap.String("subjects", "auth.user.created, auth.user.updated, auth.tenant.created, auth.tenant.updated"))
	return nil
}

func (c *AuthEventsConsumer) handleUserCreated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)
	name, _ := evt.Payload["full_name"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.created event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, evt.TenantID, userID, email, name); err != nil {
		return fmt.Errorf("sync user from auth.user.created: %w", err)
	}

	if err := c.reconcileUserOutlets(ctx, evt.TenantID, userID, evt.Payload); err != nil {
		return fmt.Errorf("reconcile outlets from auth.user.created: %w", err)
	}

	c.log.Info("user synced from auth.user.created",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", evt.TenantID.String()))
	return nil
}

func (c *AuthEventsConsumer) handleUserUpdated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	email, _ := evt.Payload["email"].(string)
	name, _ := evt.Payload["full_name"].(string)

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth.user.updated event")
	}

	if _, err := c.rbacSvc.SyncUser(ctx, evt.TenantID, userID, email, name); err != nil {
		return fmt.Errorf("sync user from auth.user.updated: %w", err)
	}

	if err := c.reconcileUserOutlets(ctx, evt.TenantID, userID, evt.Payload); err != nil {
		return fmt.Errorf("reconcile outlets from auth.user.updated: %w", err)
	}

	c.log.Info("user synced from auth.user.updated",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", evt.TenantID.String()))
	return nil
}

// handlePinSet stores the bcrypt PIN hash carried on an auth.user.pin_set event so the user
// can authenticate at an inventory terminal. Only acts on events for service "inventory".
func (c *AuthEventsConsumer) handlePinSet(ctx context.Context, evt *sharedevents.Event) error {
	service, _ := evt.Payload["service"].(string)
	if service != "" && service != "inventory" {
		return nil // PIN provisioned for a different service (pos/library/…)
	}
	userIDStr, _ := evt.Payload["user_id"].(string)
	pinHash, _ := evt.Payload["pin_hash"].(string)
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id %q in pin_set: %w", userIDStr, err)
	}
	if evt.TenantID == uuid.Nil || pinHash == "" {
		return fmt.Errorf("missing tenant_id or pin_hash in pin_set event")
	}

	// Ensure the local user exists first (pin_set may arrive before user.created is processed).
	email, _ := evt.Payload["email"].(string)
	name, _ := evt.Payload["full_name"].(string)
	if _, serr := c.rbacSvc.SyncUser(ctx, evt.TenantID, userID, email, name); serr != nil {
		return fmt.Errorf("sync user for pin_set: %w", serr)
	}

	n, uerr := c.orm.InventoryUser.Update().
		Where(entinvuser.TenantID(evt.TenantID), entinvuser.AuthServiceUserID(userID)).
		SetPinHash(pinHash).
		SetPinFailedAttempts(0).
		ClearPinLockedUntil().
		Save(ctx)
	if uerr != nil {
		return fmt.Errorf("store pin hash: %w", uerr)
	}
	c.log.Info("stored terminal PIN hash", zap.String("user_id", userID.String()),
		zap.String("tenant_id", evt.TenantID.String()), zap.Int("updated", n))
	return nil
}

// handleTenantChanged invalidates the cached tenant branding (tenant:{slug}) when a tenant's
// profile changes in auth, so generated documents pick up the new company name / address / KRA PIN
// immediately instead of serving the 6h-TTL cached copy.
func (c *AuthEventsConsumer) handleTenantChanged(ctx context.Context, evt *sharedevents.Event) error {
	if c.cache == nil {
		return nil
	}
	slug, _ := evt.Payload["slug"].(string)
	if slug == "" {
		return nil
	}
	c.cache.Invalidate(ctx, sharedcache.TenantCacheKey(slug))
	c.log.Info("invalidated tenant branding cache on auth.tenant change", zap.String("slug", slug))
	return nil
}

// reconcileUserOutlets makes the user's UserOutlet rows match the outlet set carried
// on the auth.user event, for the given tenant. The home outlet is the first entry.
//
// Idempotent and safe against partial events: when the payload carries NO outlet
// information at all (e.g. a profile-only or pin_set update), assignments are left
// untouched. An explicit empty assignment (outlet_id:"") clears them.
func (c *AuthEventsConsumer) reconcileUserOutlets(ctx context.Context, tenantID, userID uuid.UUID, payload map[string]any) error {
	if c.orm == nil {
		return nil
	}
	desired, present := desiredOutletSet(payload)
	if !present {
		return nil // event carries no outlet assignment info — don't touch existing rows
	}

	existing, err := c.orm.UserOutlet.Query().
		Where(entuseroutlet.TenantID(tenantID), entuseroutlet.UserID(userID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("load user outlets: %w", err)
	}
	existingByOutlet := make(map[uuid.UUID]*ent.UserOutlet, len(existing))
	for _, a := range existing {
		existingByOutlet[a.OutletID] = a
	}
	desiredSet := make(map[uuid.UUID]bool, len(desired))
	for _, o := range desired {
		desiredSet[o] = true
	}

	// Remove assignments that are no longer desired.
	for outletID, row := range existingByOutlet {
		if !desiredSet[outletID] {
			if delErr := c.orm.UserOutlet.DeleteOne(row).Exec(ctx); delErr != nil && !ent.IsNotFound(delErr) {
				return fmt.Errorf("remove stale user outlet: %w", delErr)
			}
		}
	}

	// Insert missing assignments. The first outlet in the list is the home outlet.
	for i, outletID := range desired {
		if _, ok := existingByOutlet[outletID]; ok {
			continue
		}
		if _, createErr := c.orm.UserOutlet.Create().
			SetTenantID(tenantID).
			SetUserID(userID).
			SetOutletID(outletID).
			SetIsHomeOutlet(i == 0).
			Save(ctx); createErr != nil && !ent.IsConstraintError(createErr) {
			return fmt.Errorf("assign user outlet: %w", createErr)
		}
	}

	c.log.Info("user-outlet assignments reconciled",
		zap.String("user_id", userID.String()),
		zap.String("tenant_id", tenantID.String()),
		zap.Int("outlets", len(desired)))
	return nil
}

// desiredOutletSet extracts the desired outlet assignment set from an auth.user
// event payload. Returns (set, true) when the payload carries assignment info —
// either outlet_ids (array, used by the seed/multi-outlet path) or outlet_id
// (single, used by the admin member-management path; "" means clear). Returns
// (nil, false) when no outlet key is present, signalling "leave assignments as-is".
func desiredOutletSet(payload map[string]any) ([]uuid.UUID, bool) {
	if raw, ok := payload["outlet_ids"]; ok {
		arr, _ := raw.([]any)
		out := make([]uuid.UUID, 0, len(arr))
		for _, v := range arr {
			if s, ok := v.(string); ok {
				if id, err := uuid.Parse(s); err == nil {
					out = append(out, id)
				}
			}
		}
		return out, true
	}
	if raw, ok := payload["outlet_id"]; ok {
		s, _ := raw.(string)
		if s == "" {
			return nil, true // explicit clear
		}
		if id, err := uuid.Parse(s); err == nil {
			return []uuid.UUID{id}, true
		}
		return nil, true
	}
	return nil, false
}
