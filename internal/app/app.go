package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	sharedcache "github.com/Bengo-Hub/cache"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	eventslib "github.com/Bengo-Hub/shared-events"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/config"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/migrate"
	handlers "github.com/bengobox/inventory-service/internal/http/handlers"
	router "github.com/bengobox/inventory-service/internal/http/router"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	backupmod "github.com/bengobox/inventory-service/internal/modules/backup"
	"github.com/bengobox/inventory-service/internal/modules/backup/destination"
	"github.com/bengobox/inventory-service/internal/modules/bundles"
	"github.com/bengobox/inventory-service/internal/modules/consumers"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
	"github.com/bengobox/inventory-service/internal/modules/tickets"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
	"github.com/bengobox/inventory-service/internal/modules/units"
	"github.com/bengobox/inventory-service/internal/platform/cache"
	"github.com/bengobox/inventory-service/internal/platform/database"
	"github.com/bengobox/inventory-service/internal/platform/events"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
	"github.com/bengobox/inventory-service/internal/services/usersync"
	"github.com/bengobox/inventory-service/internal/shared/logger"
)

type App struct {
	cfg                 *config.Config
	log                 *zap.Logger
	httpServer          *http.Server
	db                  *pgxpool.Pool
	cache               *redis.Client
	events              *nats.Conn
	orm                 *ent.Client
	outboxPublisher     *eventslib.OutboxPoller
	posSaleConsumer     *consumers.POSSaleEventsConsumer
	conferenceConsumer  *consumers.ConferenceEventsConsumer
	authConsumer        *consumers.AuthEventsConsumer
	returnConsumer      *consumers.ReturnEventsConsumer
	stockConsumer       *consumers.StockEventsConsumer
	ticketConsumer      *consumers.TicketIssuanceConsumer
	treasuryTaxConsumer *consumers.TreasuryTaxEventsConsumer
	tenantPurgeConsumer *consumers.TenantPurgeConsumer
}

func New(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	log, err := logger.New(cfg.App.Env)
	if err != nil {
		return nil, fmt.Errorf("logger init: %w", err)
	}

	dbPool, err := database.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("postgres init: %w", err)
	}

	redisClient := cache.NewClient(cfg.Redis)

	natsConn, err := events.Connect(cfg.Events)
	if err != nil {
		log.Warn("event bus connection failed", zap.Error(err))
	}

	// Ensure inventory JetStream stream exists (for stock events consumed by notifications-api etc.)
	if natsConn != nil {
		if streamErr := events.EnsureStream(ctx, natsConn, cfg.Events); streamErr != nil {
			log.Warn("failed to ensure inventory stream", zap.Error(streamErr))
		}
	}

	healthHandler := handlers.NewHealthHandler(log, dbPool, redisClient, natsConn)

	// Initialize user management services (placeholder — real wiring after Ent client)
	syncService := usersync.NewService(cfg.Auth.ServiceURL, cfg.Auth.APIKey, log)

	// Initialize Ent ORM client
	sqlDB, err := sql.Open("pgx", cfg.Postgres.URL)
	if err != nil {
		return nil, fmt.Errorf("ent driver init: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.Postgres.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	ormClient := ent.NewClient(ent.Driver(drv))

	// Run versioned migrations only when explicitly enabled.
	// In production, migrations are run by the entrypoint before the server starts.
	if cfg.Postgres.RunMigrations {
		if err := ormClient.Schema.Create(ctx,
			schema.WithDir(migrate.Dir),
		); err != nil {
			return nil, fmt.Errorf("ent schema create: %w", err)
		}
		log.Info("versioned migrations applied (POSTGRES_RUN_MIGRATIONS=true)")
	}

	// Initialize outbox background publisher (Transactional Outbox Pattern)
	var outboxPublisher *eventslib.OutboxPoller
	if natsConn != nil && cfg.Events.OutboxEnabled {
		outboxRepo := eventslib.NewSQLOutboxRepository(sqlDB)
		outboxNatsPublisher := eventslib.NewNATSAdapter(natsConn, log)
		outboxCfg := eventslib.PollerConfig{
			BatchSize:  cfg.Events.OutboxBatchSize,
			PollPeriod: cfg.Events.OutboxPollPeriod,
		}
		outboxPublisher = eventslib.NewOutboxPoller(outboxRepo, outboxNatsPublisher, log, outboxCfg)
		outboxPublisher.Start(ctx)
		log.Info("outbox background publisher started",
			zap.Int("batch_size", cfg.Events.OutboxBatchSize),
			zap.Duration("poll_period", cfg.Events.OutboxPollPeriod))
	}

	// Initialize cache helper for read-heavy queries
	cacheAside := sharedcache.New(redisClient, log)

	// Initialize RBAC module (DB-backed, replaces in-memory stub)
	rbacRepo := rbac.NewEntRepository(ormClient)
	tenantSyncer := tenant.NewSyncer(ormClient, cfg.Auth.ServiceURL)
	rbacService := rbac.NewService(rbacRepo, log, tenantSyncer)
	userHandler := handlers.NewUserHandler(log, rbacService, syncService, rbacRepo)
	rbacHandler := handlers.NewRBACHandler(log, rbacService, syncService, rbacRepo)
	authHandler := handlers.NewAuthHandler(log, rbacService)

	// Initialize business modules
	itemsSvc := items.NewService(ormClient, log, cfg.Media.URLBase)
	itemsSvc.SetCache(cacheAside)
	// Treasury is the source of truth for tax codes/rates; resolve + cache VAT rates for item
	// enrichment, and expose the cached tax-code list to inventory-ui for the tax-code picker.
	treasuryClient := treasury.NewClient(cfg.Services.TreasuryURL, cfg.Auth.APIKey, cacheAside, log)
	itemsSvc.SetTaxResolver(treasuryClient)
	stockSvc := stock.NewService(ormClient, log)
	recipeSvc := recipes.NewService(ormClient, log)
	unitSvc := units.NewService(ormClient, log)
	modifiersSvc := modifiers.NewService(ormClient, log)
	transferSvc := transfers.NewService(ormClient, log)
	ticketsSvc := tickets.NewService(ormClient, log)
	inventoryHandler := handlers.NewInventoryHandler(log, itemsSvc, stockSvc, recipeSvc, unitSvc)
	inventoryHandler.SetModifiersService(modifiersSvc)
	inventoryHandler.SetTicketsService(ticketsSvc)
	inventoryHandler.SetRBACService(rbacService)
	inventoryHandler.SetEntClient(ormClient) // for RequireOutletUseCase warehouse use_case lookup
	inventoryHandler.SetApprovalService(approvals.NewService(ormClient))
	warehouseHandler := handlers.NewWarehouseHandler(log, ormClient, rbacService)
	warehouseHandler.SetAuthURL(cfg.Auth.ServiceURL)
	auditSvc := audit.NewService(ormClient, log)
	warehouseHandler.SetAuditService(auditSvc)
	inventoryHandler.SetAuditService(auditSvc)
	stockCountHandler := handlers.NewStockCountHandler(log, ormClient, stockSvc, rbacService, auditSvc)
	warehouseLocationHandler := handlers.NewWarehouseLocationHandler(log, ormClient, rbacService)
	pricingTierHandler := handlers.NewPricingTierHandler(log, ormClient, rbacService)
	brandHandler := handlers.NewBrandHandler(log, ormClient, rbacService)
	transferHandler := handlers.NewTransferHandler(log, transferSvc, rbacService, approvals.NewService(ormClient))
	inventoryExtrasHandler := handlers.NewInventoryExtrasHandler(log, ormClient, rbacService)
	bundleSvc := bundles.NewService(ormClient, log)
	inventoryExtrasHandler.SetBundleService(bundleSvc)
	varianceSvc := recipes.NewVarianceService(ormClient, log, cfg.Services.OrderingURL, cfg.Auth.APIKey)
	inventoryExtrasHandler.SetVarianceService(varianceSvc)
	menuEngSvc := recipes.NewMenuEngineeringService(ormClient, log, cfg.Services.OrderingURL, cfg.Auth.APIKey)
	inventoryExtrasHandler.SetMenuEngineeringService(menuEngSvc)
	docSvc := documents.NewService(ormClient, cacheAside, cfg.Auth.ServiceURL, log)
	inventoryExtrasHandler.SetDocService(docSvc)
	inventoryHandler.SetDocService(docSvc) // branded event-ticket PDFs (with QR)
	inventoryExtrasHandler.SetStockService(stockSvc)
	// Optional automated month-end depreciation (opt-in; depreciation_rate must be a
	// per-month fraction when enabled — see StartDepreciationScheduler). Off by default;
	// most tenants run depreciation manually at period close.
	if os.Getenv("ASSET_DEPRECIATION_SCHEDULER") == "true" {
		go inventoryExtrasHandler.StartDepreciationScheduler(ctx)
		log.Info("asset depreciation scheduler enabled")
	}
	analyticsHandler := handlers.NewAnalyticsHandler(log, ormClient)
	handlers.SetTenantDB(ormClient)        // Enable local slug-to-UUID lookups
	handlers.SetTenantSyncer(tenantSyncer) // Enable slug-to-UUID resolution via auth-api

	// Subscriptions S2S client + gate: restrict cross-service stock sync (ordering/POS →
	// inventory) to tenants entitled to basic_inventory_access. Cached per tenant; fails open
	// on a subscriptions-api outage so a downtime never silently halts stock movements.
	subsClient := subscriptions.NewClient(subscriptions.Config{
		ServiceURL:     cfg.Subscriptions.ServiceURL,
		APIKey:         cfg.Subscriptions.APIKey,
		RequestTimeout: cfg.Subscriptions.RequestTimeout,
	})
	consumerFeatureGate := func(ctx context.Context, tenantID, feature string) bool {
		return subsClient.ConsumerHasFeature(ctx, tenantID, feature)
	}

	// POS sale events consumer — consume stock on pos.sale.finalized (with BOM explosion)
	posSaleConsumer := consumers.NewPOSSaleEventsConsumer(log, stockSvc, ormClient)
	posSaleConsumer.SetFeatureGate(consumerFeatureGate)
	conferenceConsumer := consumers.NewConferenceEventsConsumer(log, stockSvc, ormClient)
	ticketConsumer := consumers.NewTicketIssuanceConsumer(log, ticketsSvc, ormClient)

	// Auth events consumer — proactive user sync + UserOutlet assignment projection from auth-service
	authConsumer := consumers.NewAuthEventsConsumer(log, rbacService, ormClient)

	// Return events consumer — restock inventory on pos.return.completed + ordering.return.approved
	returnConsumer := consumers.NewReturnEventsConsumer(log, stockSvc)

	// Stock low events consumer — auto-creates draft POs when auto_reorder_enabled
	stockConsumer := consumers.NewStockEventsConsumer(log, ormClient)

	// Treasury tax-code change consumer — invalidates cached tax data so rate changes propagate immediately
	treasuryTaxConsumer := consumers.NewTreasuryTaxEventsConsumer(log, treasuryClient)

	// Tenant purge consumer — on platform-owner-confirmed dormancy purge (tenant.purge),
	// IRREVERSIBLY deletes ALL of the tenant's inventory data. Uses the raw *sql.DB for
	// FK-order-independent, transactional deletes.
	tenantPurgeConsumer := consumers.NewTenantPurgeConsumer(log, sqlDB)

	// Initialize auth-service JWT validator
	var authMiddleware *authclient.AuthMiddleware
	authConfig := authclient.DefaultConfig(
		cfg.Auth.JWKSUrl,
		cfg.Auth.Issuer,
		cfg.Auth.Audience,
	)
	authConfig.CacheTTL = cfg.Auth.JWKSCacheTTL
	authConfig.RefreshInterval = cfg.Auth.JWKSRefreshInterval

	validator, err := authclient.NewValidator(authConfig)
	if err != nil {
		return nil, fmt.Errorf("auth validator init: %w", err)
	}

	if cfg.Auth.EnableAPIKeyAuth {
		apiKeyValidator := authclient.NewAPIKeyValidator(cfg.Auth.ServiceURL, nil)
		authMiddleware = authclient.NewAuthMiddlewareWithAPIKey(validator, apiKeyValidator)
	} else {
		authMiddleware = authclient.NewAuthMiddleware(validator)
	}

	// Initialize NATS event subscribers for proactive provisioning
	branchSub := tenant.NewBranchSubscriber(ormClient, log)
	if err := branchSub.Start(natsConn); err != nil {
		log.Warn("app: failed to start outlet event subscriptions", zap.Error(err))
	}
	warehouseHandler.SetOutletSyncer(branchSub)

	// Startup reconciliation: catch any outlet events missed while the pod was down.
	// Runs once 15 seconds after startup so NATS consumers are warm first.
	authURLForSync := strings.TrimRight(cfg.Auth.ServiceURL, "/")
	go func() {
		select {
		case <-time.After(15 * time.Second):
		case <-ctx.Done():
			return
		}
		if err := branchSub.ReconcileFromAuthAPI(ctx, authURLForSync, ""); err != nil {
			log.Warn("app: outlet startup reconciliation failed", zap.Error(err))
		}
	}()

	if natsConn != nil {
		subCacheSub := subscriptions.NewCacheSubscriber(redisClient, log)
		if err := subCacheSub.Start(natsConn); err != nil {
			log.Warn("app: failed to start subscription cache subscriber", zap.Error(err))
		}
	}

	// Initialize media handler for file uploads
	var mediaHandler *handlers.MediaHandler
	if cfg.Media.Root != "" {
		mediaHandler = handlers.NewMediaHandler(log, cfg.Media)
	}

	// Initialize service config handler for platform admin + tenant settings
	serviceConfigHandler := handlers.NewServiceConfigHandler(ormClient, log)

	// Typed tenant inventory config (thresholds, module toggles, tracking settings)
	inventorySettingsHandler := handlers.NewInventorySettingsHandler(log, ormClient)
	inventorySettingsHandler.SetRBACService(rbacService)
	inventorySettingsHandler.SetTreasuryClient(treasuryClient)

	// Tenant-scoped backups + daily 02:00 auto-backup scheduler + retention churn.
	backupSvc := backupmod.NewService(sqlDB, ormClient, cfg.Backup.Dir, log)
	// Pluggable backup destination (PVC primary + best-effort rclone remote mirror).
	// Secret backend params are encrypted at rest with a SECRET_KEY-derived AES key.
	backupDestHandler := handlers.NewBackupDestinationHandler(ormClient, destination.NewSecretKeyCipher(), rbacService, log)
	// Mirror every freshly-written PVC backup to the resolved remote, best-effort.
	backupSvc = backupSvc.WithMirrorer(backupDestHandler.Uploader())
	backupsHandler := handlers.NewBackups(log, backupSvc, rbacService, cfg.Backup.RetentionDays)
	backupmod.NewScheduler(backupSvc, backupmod.SchedulerConfig{
		Enabled:       cfg.Backup.ScheduleEnabled,
		Hour:          cfg.Backup.ScheduleHour,
		RetentionDays: cfg.Backup.RetentionDays,
	}, log).Start(ctx)

	chiRouter := router.New(log, healthHandler, userHandler, inventoryHandler, warehouseHandler, warehouseLocationHandler, pricingTierHandler, brandHandler, transferHandler, inventoryExtrasHandler, analyticsHandler, rbacHandler, authHandler, authMiddleware, tenantSyncer, rbacService, cfg.HTTP.AllowedOrigins, mediaHandler, cfg.Media.Root, serviceConfigHandler, inventorySettingsHandler, redisClient, ormClient, stockCountHandler, backupsHandler, backupDestHandler)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:                 cfg,
		log:                 log,
		httpServer:          httpServer,
		db:                  dbPool,
		cache:               redisClient,
		events:              natsConn,
		orm:                 ormClient,
		outboxPublisher:     outboxPublisher,
		posSaleConsumer:     posSaleConsumer,
		conferenceConsumer:  conferenceConsumer,
		authConsumer:        authConsumer,
		returnConsumer:      returnConsumer,
		stockConsumer:       stockConsumer,
		ticketConsumer:      ticketConsumer,
		treasuryTaxConsumer: treasuryTaxConsumer,
		tenantPurgeConsumer: tenantPurgeConsumer,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	// Start auth events consumer for proactive user sync from auth-service
	if a.authConsumer != nil && a.events != nil {
		if err := a.authConsumer.Start(ctx, a.events); err != nil {
			a.log.Warn("auth events consumer not started", zap.Error(err))
		} else {
			a.log.Info("auth events consumer started")
		}
	}

	// Ordering->inventory stock is reconciled via direct S2S calls (consumeOrderReservation on
	// completion / releaseOrderReservation on cancellation in ordering-backend — the single,
	// intended deduction path), NOT via order lifecycle events. There is deliberately no
	// event-based order consumer: it never received events (ordering writes its order_events to a
	// DB audit table, not NATS) and, if wired, would double-deduct against the S2S path.
	if a.events != nil {
		js, err := a.events.JetStream()
		if err != nil {
			a.log.Warn("jetstream unavailable, downstream event consumers not started", zap.Error(err))
		} else {
			// Start POS sale events consumer
			if a.posSaleConsumer != nil {
				go func() {
					if err := a.posSaleConsumer.Start(ctx, js); err != nil {
						a.log.Error("pos sale events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("pos sale events consumer started")
			}

			// Start conference meal-card redemption consumer (backflush meal BOM)
			if a.conferenceConsumer != nil {
				go func() {
					if err := a.conferenceConsumer.Start(ctx, js); err != nil {
						a.log.Error("conference events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("conference meal-card events consumer started")
			}

			// Start ticket issuance consumer (ordering.order.payment_confirmed -> issue event tickets)
			if a.ticketConsumer != nil {
				go func() {
					if err := a.ticketConsumer.Start(ctx, js); err != nil {
						a.log.Error("ticket issuance consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("ticket issuance consumer started")
			}

			// Start return events consumers (pos.return.completed + ordering.return.approved)
			if a.returnConsumer != nil {
				go func() {
					if err := a.returnConsumer.StartPOSReturns(ctx, js); err != nil {
						a.log.Error("pos return events consumer stopped", zap.Error(err))
					}
				}()
				go func() {
					if err := a.returnConsumer.StartOrderingReturns(ctx, js); err != nil {
						a.log.Error("ordering return events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("return events consumers started")
			}

			// Start stock low events consumer — auto-creates draft POs when auto_reorder_enabled
			if a.stockConsumer != nil {
				go func() {
					if err := a.stockConsumer.Start(ctx, js); err != nil {
						a.log.Error("stock events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("stock events consumer started")
			}

			// Start treasury tax-code change consumer — invalidates cached treasury tax data
			if a.treasuryTaxConsumer != nil {
				go func() {
					if err := a.treasuryTaxConsumer.Start(ctx, js); err != nil {
						a.log.Error("treasury tax events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("treasury tax events consumer started")
			}

			// Start tenant purge consumer — IRREVERSIBLY deletes a tenant's data on a
			// platform-owner-confirmed dormancy purge (tenant.purge).
			if a.tenantPurgeConsumer != nil {
				go func() {
					if err := a.tenantPurgeConsumer.Start(ctx, js); err != nil {
						a.log.Error("tenant purge consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("tenant purge consumer started")
			}
		}
	}

	errCh := make(chan error, 1)
	if a.cfg.HTTP.TLSCertFile != "" && a.cfg.HTTP.TLSKeyFile != "" {
		a.log.Info("inventory service starting with HTTPS",
			zap.String("addr", a.httpServer.Addr),
			zap.String("cert", a.cfg.HTTP.TLSCertFile),
			zap.String("key", a.cfg.HTTP.TLSKeyFile),
		)
		go func() {
			errCh <- a.httpServer.ListenAndServeTLS(a.cfg.HTTP.TLSCertFile, a.cfg.HTTP.TLSKeyFile)
		}()
	} else {
		a.log.Info("inventory service starting with HTTP", zap.String("addr", a.httpServer.Addr))
		go func() {
			errCh <- a.httpServer.ListenAndServe()
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}

		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server error: %w", err)
	}
}

func (a *App) Close() {
	// Stop outbox publisher first (before NATS connection)
	if a.outboxPublisher != nil {
		a.outboxPublisher.Stop()
		a.log.Info("outbox publisher stopped")
	}

	if a.events != nil {
		if err := a.events.Drain(); err != nil {
			a.log.Warn("nats drain failed", zap.Error(err))
		}
		a.events.Close()
	}

	if a.cache != nil {
		if err := a.cache.Close(); err != nil {
			a.log.Warn("redis close failed", zap.Error(err))
		}
	}

	if a.db != nil {
		a.db.Close()
	}

	if a.orm != nil {
		if err := a.orm.Close(); err != nil {
			a.log.Warn("ent client close failed", zap.Error(err))
		}
	}

	_ = a.log.Sync()
}
