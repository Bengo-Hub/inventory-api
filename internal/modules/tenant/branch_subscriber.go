package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// inventoryAcceptedUseCases is the set of outlet use_cases whose outlets get a warehouse mirror.
// Logistics hubs and weighbridge stations don't hold inventory stock.
var inventoryAcceptedUseCases = map[string]bool{
	"hospitality":   true,
	"retail":        true,
	"quick_service": true,
	"pharmacy":      true,
	"services":      true,
	"warehouse":     true,
	"manufacturing": true,
}

const authStream = "auth"

// IsInventoryApplicable reports whether an outlet of the given use_case is relevant to
// inventory-api (i.e. it gets a warehouse mirror). Mirrors auth's ApplicableServices for
// inventory-api. An empty use_case is treated as applicable (legacy/unknown — don't hide).
// Used by the outlet event filter AND the /my-outlets listing so non-inventory outlets
// (logistics, weighbridge, enforcement) never surface in inventory's select-outlet.
func IsInventoryApplicable(useCase string) bool {
	return useCase == "" || inventoryAcceptedUseCases[useCase]
}

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
		sharedevents.SubscribeQueueWithRebind(s.logger, js, authStream, cfg.subject, cfg.durable, func(msg *nats.Msg) {
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
		)
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

	var wh *ent.Warehouse
	if ent.IsNotFound(err) {
		wh, err = s.orm.Warehouse.Create().
			SetTenantID(evt.TenantID).
			SetOutletID(outletID).
			SetName(name).
			SetCode(whCode).
			SetUseCase(useCase).
			SetIsDefault(isHQ).
			SetIsActive(status != "archived").
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create warehouse mirror: %w", err)
		}
		s.logger.Info("warehouse created from auth.outlet event",
			zap.String("outlet_id", outletIDStr),
			zap.String("code", whCode),
			zap.String("use_case", useCase))
	} else {
		upd := s.orm.Warehouse.UpdateOne(existing).
			SetName(name).
			SetIsDefault(isHQ).
			SetIsActive(status != "archived")
		// Keep the use_case mirror current, but never clobber a known value with an
		// empty one if a future event omits it.
		if useCase != "" {
			upd = upd.SetUseCase(useCase)
		}
		wh, err = upd.Save(ctx)
		if err != nil {
			return fmt.Errorf("update warehouse mirror: %w", err)
		}
		s.logger.Info("warehouse updated from auth.outlet event",
			zap.String("outlet_id", outletIDStr),
			zap.String("code", existing.Code))
	}

	// Enforce the single-default invariant: a tenant may have at most one default
	// warehouse. When this (HQ) outlet's warehouse is the default, clear is_default on
	// every OTHER warehouse of the tenant — otherwise the seed's MAIN default plus the
	// HQ outlet's default leave two, which breaks default-warehouse resolution (stock
	// consumption / "no default warehouse for tenant").
	if isHQ && wh != nil {
		if n, derr := s.orm.Warehouse.Update().
			Where(
				entwarehouse.TenantID(evt.TenantID),
				entwarehouse.IDNEQ(wh.ID),
				entwarehouse.IsDefault(true),
			).
			SetIsDefault(false).
			Save(ctx); derr != nil {
			s.logger.Warn("failed to clear other default warehouses", zap.Error(derr))
		} else if n > 0 {
			s.logger.Info("cleared other default warehouses to enforce single default",
				zap.Int("cleared", n), zap.String("kept", wh.Code))
		}
	}
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

// authOutlet is the minimal shape of an outlet returned by auth-api.
type authOutlet struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	UseCase string `json:"use_case"`
	IsHQ    bool   `json:"is_hq"`
	Status  string `json:"status"`
}

// authOutletListResponse wraps the array response from GET /tenants/{slug}/outlets.
// Auth-api returns a JSON array directly.
type authOutletListResponse = []authOutlet

// ReconcileFromAuthAPI fetches outlets from auth-api for every known tenant and
// creates missing warehouse mirrors.
//
// Flow:
//  1. List local tenants from inventory DB.
//  2. For each, call the public GET /tenants/by-slug/{slug} to confirm the tenant
//     is active (no auth required).
//  3. Call the public GET /tenants/{slug}/outlets (now public — no auth required).
//  4. For each outlet not yet mirrored as a warehouse, call handleUpsert.
//
// authToken is kept for backward compatibility but is no longer required since the
// outlet listing endpoint was made public.
func (s *BranchSubscriber) ReconcileFromAuthAPI(ctx context.Context, authURL, authToken string) error {
	if authURL == "" {
		return fmt.Errorf("reconcile: authURL is empty")
	}
	tenants, err := s.orm.Tenant.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list tenants: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	synced, skipped, failed := 0, 0, 0

	for _, t := range tenants {
		// Step 1: verify tenant exists in auth-api via the public by-slug endpoint
		// (no auth required — used to confirm slug is valid before hitting outlets).
		verifyURL := fmt.Sprintf("%s/api/v1/tenants/by-slug/%s", authURL, t.Slug)
		vReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
		vResp, vErr := client.Do(vReq)
		if vErr != nil || vResp.StatusCode != http.StatusOK {
			if vResp != nil {
				vResp.Body.Close()
			}
			s.logger.Debug("reconcile: tenant not found in auth-api, skipping",
				zap.String("slug", t.Slug))
			skipped++
			continue
		}
		io.Copy(io.Discard, vResp.Body)
		vResp.Body.Close()

		// Step 2: fetch outlets using the platform-owner JWT (required by auth-api).
		outletsURL := fmt.Sprintf("%s/api/v1/tenants/%s/outlets", authURL, t.Slug)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, outletsURL, nil)
		if err != nil {
			failed++
			continue
		}
		if authToken != "" {
			req.Header.Set("Authorization", authToken)
		}
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			s.logger.Warn("reconcile: could not list outlets",
				zap.String("tenant", t.Slug), zap.NamedError("err", err))
			failed++
			continue
		}
		var outlets authOutletListResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&outlets); decErr != nil {
			resp.Body.Close()
			s.logger.Warn("reconcile: decode outlets failed",
				zap.String("tenant", t.Slug), zap.Error(decErr))
			failed++
			continue
		}
		resp.Body.Close()

		// Step 3: upsert warehouse mirrors for applicable outlets only.
		for _, o := range outlets {
			// Skip outlets whose use_case doesn't apply to inventory warehouses
			// (same filter used by the NATS event handler).
			if o.UseCase != "" && !inventoryAcceptedUseCases[o.UseCase] {
				s.logger.Debug("reconcile: skipping outlet — use_case not applicable",
					zap.String("outlet_id", o.ID), zap.String("use_case", o.UseCase))
				skipped++
				continue
			}
			evt := &sharedevents.Event{
				TenantID: t.ID,
				Payload: map[string]any{
					"outlet_id": o.ID,
					"code":      o.Code,
					"name":      o.Name,
					"use_case":  o.UseCase,
					"is_hq":     o.IsHQ,
					"status":    o.Status,
				},
			}
			if uErr := s.handleUpsert(ctx, evt); uErr != nil {
				s.logger.Warn("reconcile: upsert failed",
					zap.String("outlet_id", o.ID), zap.Error(uErr))
				failed++
			} else {
				synced++
			}
		}
	}
	s.logger.Info("outlet reconciliation complete",
		zap.Int("synced", synced), zap.Int("skipped", skipped), zap.Int("failed", failed))
	return nil
}

// SyncTenantOutlets reconciles outlets for a single tenant slug.
// Useful for a targeted fix (e.g., sync only urban-loft after initial setup).
func (s *BranchSubscriber) SyncTenantOutlets(ctx context.Context, authURL, authToken, slug string) error {
	t, err := s.orm.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if err != nil {
		return fmt.Errorf("sync tenant outlets: tenant %q not found: %w", slug, err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	outletsURL := fmt.Sprintf("%s/api/v1/tenants/%s/outlets", authURL, slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, outletsURL, nil)
	if err != nil {
		return err
	}
	if authToken != "" {
		req.Header.Set("Authorization", authToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch outlets: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth-api returned %d for %s outlets", resp.StatusCode, slug)
	}
	var outlets authOutletListResponse
	if err := json.NewDecoder(resp.Body).Decode(&outlets); err != nil {
		return fmt.Errorf("decode outlets: %w", err)
	}
	synced := 0
	for _, o := range outlets {
		if o.UseCase != "" && !inventoryAcceptedUseCases[o.UseCase] {
			s.logger.Debug("sync tenant outlets: skipping — use_case not applicable",
				zap.String("outlet_id", o.ID), zap.String("use_case", o.UseCase))
			continue
		}
		evt := &sharedevents.Event{
			TenantID: t.ID,
			Payload: map[string]any{
				"outlet_id": o.ID,
				"code":      o.Code,
				"name":      o.Name,
				"use_case":  o.UseCase,
				"is_hq":     o.IsHQ,
				"status":    o.Status,
			},
		}
		if uErr := s.handleUpsert(ctx, evt); uErr != nil {
			s.logger.Warn("sync tenant outlets: upsert failed",
				zap.String("outlet_id", o.ID), zap.Error(uErr))
		} else {
			synced++
		}
	}
	s.logger.Info("sync tenant outlets: done",
		zap.String("slug", slug), zap.Int("outlets", len(outlets)), zap.Int("synced", synced))
	return nil
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
