package consumers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	// tenantPurgeStream is the JetStream stream that carries the "tenant.*" aggregate
	// events published by subscriptions-api (subject = aggregate_type.event_type, so
	// aggregate_type="tenant" + event_type="purge" => subject "tenant.purge"). It is
	// ensured here so this consumer is ready even if the publisher's pod hasn't created
	// the stream yet (same defensive pattern as the auth/treasury consumers).
	tenantPurgeStream         = "tenant"
	tenantPurgeSubject        = "tenant.purge"
	tenantPurgeDurable        = "inventory-tenant-purge"
	tenantPurgeAckWait        = 60 * time.Second
	tenantPurgeMaxDeliver     = 5
	tenantPurgeDeleteAttempts = 50 // bound the FK savepoint-retry passes
)

// tenantScopedTables is the exhaustive list of inventory-api owned tables that carry a
// tenant_id column, derived from the ent migration schema. Every row a tenant owns lives
// in one of these. They are deleted strictly scoped by tenant_id.
//
// outbox_events is intentionally EXCLUDED: wiping our own outbox while events (including
// this purge's downstream effects) are mid-delivery would corrupt the transactional
// outbox. The tenants shadow row is handled separately (keyed by id, not tenant_id).
//
// Deletion is order-independent: each table is deleted inside its own savepoint and any
// table that fails on a foreign-key violation is retried on a later pass until the set
// stabilises. Every FK child of a tenant-scoped table is itself tenant-scoped (verified
// against the schema), so the whole graph is reachable by tenant_id alone.
var tenantScopedTables = []string{
	"approval_actions",
	"approval_requests",
	"approval_rules",
	"approval_steps",
	"asset_audits",
	"asset_categories",
	"asset_disposals",
	"asset_insurances",
	"asset_maintenances",
	"asset_reservations",
	"asset_transfers",
	"assets",
	"audit_logs",
	"backup_settings",
	"backups",
	"batch_raw_materials",
	"bundles",
	"consumptions",
	"contract_order_links",
	"contracts",
	"custom_field_definitions",
	"document_sequences",
	"food_cost_variances",
	"goods_receipt_lines",
	"goods_receipts",
	"inventory_balances",
	"inventory_lots",
	"inventory_roles",
	"inventory_users",
	"item_brands",
	"item_categories",
	"item_pricings",
	"items",
	"manufacturing_analytics",
	"modifier_groups",
	"pricing_tiers",
	"production_batches",
	"purchase_orders",
	"purchase_return_lines",
	"purchase_returns",
	"quality_checks",
	"raw_material_usages",
	"recipes",
	"requisition_lines",
	"requisitions",
	"reservations",
	"rf_qs",
	"rfq_awards",
	"rfq_lines",
	"service_configs",
	"service_deliveries",
	"stock_adjustments",
	"stock_breakdowns",
	"stock_count_lines",
	"stock_counts",
	"stock_transfers",
	"supplier_performances",
	"supplier_responses",
	"suppliers",
	"tenant_inventory_configs",
	"tickets",
	"user_outlets",
	"user_role_assignments",
	"variant_attributes",
	"warehouse_locations",
	"warehouses",
	"warranties",
}

// TenantPurgeConsumer deletes ALL of a tenant's inventory data when subscriptions-api
// emits a platform-owner-confirmed dormancy purge (subject "tenant.purge"). This is
// IRREVERSIBLE, so the handler is defensive: it only acts on a well-formed event that is
// explicitly confirmed=true and reason="dormancy", and it never issues a delete with an
// empty/zero tenant filter.
type TenantPurgeConsumer struct {
	log *zap.Logger
	db  *sql.DB
}

// NewTenantPurgeConsumer creates the consumer. db is the same *sql.DB backing the ent
// client; raw SQL is used so deletes are FK-order-independent (savepoint-per-table).
func NewTenantPurgeConsumer(log *zap.Logger, db *sql.DB) *TenantPurgeConsumer {
	return &TenantPurgeConsumer{
		log: log.Named("consumers.tenant_purge"),
		db:  db,
	}
}

// Start ensures the "tenant" stream exists and binds the durable purge consumer.
func (c *TenantPurgeConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if js == nil {
		c.log.Warn("jetstream unavailable, tenant purge consumer not started")
		return nil
	}

	// Ensure the "tenant" stream exists. It's normally created by subscriptions-api, but
	// may not exist yet; create it so the durable consumer can bind on startup.
	if _, err := js.StreamInfo(tenantPurgeStream); err != nil {
		c.log.Info("tenant stream not found, creating it for consumer readiness")
		if _, addErr := js.AddStream(&nats.StreamConfig{
			Name:      tenantPurgeStream,
			Subjects:  []string{"tenant.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); addErr != nil && !errors.Is(addErr, nats.ErrStreamNameAlreadyInUse) {
			return fmt.Errorf("tenant purge events: ensure stream: %w", addErr)
		}
	}

	eventslib.SubscribeWithRebind(
		c.log,
		js,
		tenantPurgeSubject,
		c.handleMessage,
		nats.Durable(tenantPurgeDurable),
		nats.AckExplicit(),
		nats.AckWait(tenantPurgeAckWait),
		nats.MaxDeliver(tenantPurgeMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("tenant purge consumer started",
		zap.String("subject", tenantPurgeSubject),
		zap.String("durable", tenantPurgeDurable))

	<-ctx.Done()
	return nil
}

// purgeEnvelope is the minimal shape of the shared-events Event we need. We read the
// inner payload defensively (confirmed/reason/tenant_id) rather than trusting top-level
// fields alone.
type purgeEnvelope struct {
	EventType string         `json:"event_type"`
	TenantID  string         `json:"tenant_id"`
	Payload   map[string]any `json:"payload"`
}

func (c *TenantPurgeConsumer) handleMessage(msg *nats.Msg) {
	var env purgeEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		// Malformed message — never retry, it will never become valid.
		c.log.Error("tenant purge: unmarshal failed, acking to drop", zap.Error(err))
		_ = msg.Ack()
		return
	}

	// SAFETY GUARD 1: resolve a non-empty, valid tenant_id. Prefer the envelope's
	// tenant_id; fall back to payload.tenant_id. An empty/zero tenant filter would wipe
	// the whole table — we must NEVER do that. Ack + do nothing.
	tenantIDStr := env.TenantID
	if tenantIDStr == "" || tenantIDStr == uuid.Nil.String() {
		if env.Payload != nil {
			if s, _ := env.Payload["tenant_id"].(string); s != "" {
				tenantIDStr = s
			}
		}
	}
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil || tenantID == uuid.Nil {
		c.log.Error("tenant purge: empty/invalid tenant_id — refusing to delete, acking to drop",
			zap.String("raw_tenant_id", tenantIDStr))
		_ = msg.Ack()
		return
	}

	// SAFETY GUARD 2: only act on a platform-owner-confirmed dormancy purge.
	confirmed, _ := env.Payload["confirmed"].(bool)
	reason, _ := env.Payload["reason"].(string)
	if !confirmed || reason != "dormancy" {
		c.log.Warn("tenant purge: not a confirmed dormancy purge — skipping (acking)",
			zap.String("tenant_id", tenantID.String()),
			zap.Bool("confirmed", confirmed),
			zap.String("reason", reason))
		_ = msg.Ack()
		return
	}

	c.log.Warn("tenant purge: IRREVERSIBLE delete starting for confirmed dormancy purge",
		zap.String("tenant_id", tenantID.String()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	total, err := c.purgeTenant(ctx, tenantID)
	if err != nil {
		// Transient DB error — Nak so JetStream redelivers (bounded by MaxDeliver). The
		// delete is transactional + idempotent, so a redelivery re-runs cleanly.
		c.log.Error("tenant purge: delete failed, nacking for redelivery",
			zap.String("tenant_id", tenantID.String()), zap.Error(err))
		_ = msg.Nak()
		return
	}

	c.log.Warn("tenant purge: completed",
		zap.String("tenant_id", tenantID.String()),
		zap.Int64("rows_deleted", total))
	_ = msg.Ack()
}

// purgeTenant deletes every row owned by tenantID across all tenant-scoped tables plus
// the tenant shadow row, inside one transaction. Returns total rows deleted.
//
// Deletes are order-independent: each table runs inside a SAVEPOINT; a table that fails
// on a foreign-key violation (because a child in the same set isn't gone yet) is retried
// on a later pass. The loop runs until every table succeeds with no further FK blocks, or
// a non-FK error / the attempt bound aborts the whole transaction (nothing is committed).
func (c *TenantPurgeConsumer) purgeTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	if tenantID == uuid.Nil {
		// Defense in depth: should be unreachable past the handler guard.
		return 0, errors.New("refusing to purge with nil tenant_id")
	}

	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin purge tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	pending := make([]string, len(tenantScopedTables))
	copy(pending, tenantScopedTables)

	var total int64
	for attempt := 0; len(pending) > 0 && attempt < tenantPurgeDeleteAttempts; attempt++ {
		var still []string
		progressed := false
		for _, table := range pending {
			n, derr := c.deleteOne(ctx, tx, table, tenantID)
			if derr != nil {
				if isForeignKeyViolation(derr) {
					// A child row in another tenant-scoped table still references this one.
					// Retry on the next pass once that child has been deleted.
					still = append(still, table)
					continue
				}
				return 0, fmt.Errorf("delete from %s: %w", table, derr)
			}
			progressed = true
			total += n
			if n > 0 {
				c.log.Info("tenant purge: table cleared",
					zap.String("tenant_id", tenantID.String()),
					zap.String("table", table),
					zap.Int64("rows", n))
			}
		}
		pending = still
		if !progressed && len(pending) > 0 {
			// No table succeeded this pass — a genuine FK cycle/unexpected reference we
			// can't resolve by retrying. Abort without committing.
			return 0, fmt.Errorf("tenant purge stalled with %d tables remaining (fk dependency unresolved): %v", len(pending), pending)
		}
	}
	if len(pending) > 0 {
		return 0, fmt.Errorf("tenant purge exceeded %d passes, %d tables remaining: %v", tenantPurgeDeleteAttempts, len(pending), pending)
	}

	// Finally remove the tenant shadow row itself (keyed by id, not tenant_id). All child
	// rows are gone above, so this is safe. Done last so a failure rolls back everything.
	res, err := tx.ExecContext(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	if err != nil {
		return 0, fmt.Errorf("delete tenant shadow row: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		total += n
		c.log.Info("tenant purge: tenant shadow row removed",
			zap.String("tenant_id", tenantID.String()))
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit purge tx: %w", err)
	}
	return total, nil
}

// deleteOne deletes a single table's rows for the tenant inside a savepoint so that a
// foreign-key failure can be rolled back to the savepoint and retried without aborting
// the whole transaction.
func (c *TenantPurgeConsumer) deleteOne(ctx context.Context, tx *sql.Tx, table string, tenantID uuid.UUID) (int64, error) {
	const sp = "tenant_purge_sp"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+sp); err != nil {
		return 0, fmt.Errorf("savepoint: %w", err)
	}
	// table names are from a fixed internal allowlist (never user input) — safe to interpolate.
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %q WHERE tenant_id = $1`, table), tenantID)
	if err != nil {
		// Roll back just this table; the tx stays usable for the next table/pass.
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+sp)
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+sp); err != nil {
		return 0, fmt.Errorf("release savepoint: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// isForeignKeyViolation reports whether err is a Postgres foreign-key violation
// (SQLSTATE 23503), detected without importing a driver-specific error type by matching
// the well-known code/text the pgx driver surfaces.
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgconn.PgError implements this method; avoid a hard dependency by using an interface.
	type sqlStater interface{ SQLState() string }
	var pgErr sqlStater
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23503"
	}
	return false
}
