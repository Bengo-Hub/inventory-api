package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
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

	"github.com/bengobox/inventory-service/internal/config"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/migrate"
	handlers "github.com/bengobox/inventory-service/internal/http/handlers"
	router "github.com/bengobox/inventory-service/internal/http/router"
	"github.com/bengobox/inventory-service/internal/modules/consumers"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/outbox"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
	"github.com/bengobox/inventory-service/internal/modules/units"
	"github.com/bengobox/inventory-service/internal/platform/cache"
	"github.com/bengobox/inventory-service/internal/platform/database"
	"github.com/bengobox/inventory-service/internal/platform/events"
	"github.com/bengobox/inventory-service/internal/services/usersync"
	"github.com/bengobox/inventory-service/internal/shared/logger"
)

type App struct {
	cfg             *config.Config
	log             *zap.Logger
	httpServer      *http.Server
	db              *pgxpool.Pool
	cache           *redis.Client
	events          *nats.Conn
	orm             *ent.Client
	outboxPublisher  *outbox.Publisher
	orderConsumer    *consumers.OrderEventsConsumer
	posSaleConsumer  *consumers.POSSaleEventsConsumer
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

	// Run versioned migrations on startup
	if err := ormClient.Schema.Create(ctx, 
		schema.WithDir(migrate.Dir),
	); err != nil {
		return nil, fmt.Errorf("ent schema create: %w", err)
	}
	log.Info("versioned migrations completed")

	// Initialize outbox background publisher (Transactional Outbox Pattern)
	var outboxPublisher *outbox.Publisher
	if natsConn != nil && cfg.Events.OutboxEnabled {
		outboxRepo := eventslib.NewSQLOutboxRepository(sqlDB)
		outboxNatsPublisher := events.NewOutboxPublisher(natsConn, log)
		outboxCfg := outbox.PublisherConfig{
			BatchSize:  cfg.Events.OutboxBatchSize,
			PollPeriod: cfg.Events.OutboxPollPeriod,
		}
		outboxPublisher = outbox.NewPublisher(outboxRepo, outboxNatsPublisher, log, outboxCfg)
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
	userHandler := handlers.NewUserHandler(log, rbacService, syncService)
	rbacHandler := handlers.NewRBACHandler(log, rbacService, syncService, rbacRepo)

	// Initialize business modules
	itemsSvc := items.NewService(ormClient, log)
	itemsSvc.SetCache(cacheAside)
	stockSvc := stock.NewService(ormClient, log)
	recipeSvc := recipes.NewService(ormClient, log)
	unitSvc := units.NewService(ormClient, log)
	inventoryHandler := handlers.NewInventoryHandler(log, itemsSvc, stockSvc, recipeSvc, unitSvc)
	handlers.SetTenantDB(ormClient)           // Enable local slug-to-UUID lookups
	handlers.SetTenantSyncer(tenantSyncer)    // Enable slug-to-UUID resolution via auth-api

	// Order events consumer — auto-consume/release reservations on order lifecycle
	orderConsumer := consumers.NewOrderEventsConsumer(log, stockSvc, ormClient)

	// POS sale events consumer — consume stock on pos.sale.finalized (with BOM explosion)
	posSaleConsumer := consumers.NewPOSSaleEventsConsumer(log, stockSvc, ormClient)

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
	if natsConn != nil {
		eventSub := events.NewSubscriber(natsConn, log)
		branchSub := tenant.NewBranchSubscriber(ormClient, log)
		if err := branchSub.RegisterSubscribers(eventSub); err != nil {
			log.Error("failed to register branch subscribers", zap.Error(err))
		}
	}

	// Initialize media handler for file uploads
	var mediaHandler *handlers.MediaHandler
	if cfg.Media.Root != "" {
		mediaHandler = handlers.NewMediaHandler(log, cfg.Media)
	}

	chiRouter := router.New(log, healthHandler, userHandler, inventoryHandler, rbacHandler, authMiddleware, tenantSyncer, rbacService, cfg.HTTP.AllowedOrigins, mediaHandler, cfg.Media.Root)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
		Handler:           chiRouter,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	return &App{
		cfg:             cfg,
		log:             log,
		httpServer:      httpServer,
		db:              dbPool,
		cache:           redisClient,
		events:          natsConn,
		orm:             ormClient,
		outboxPublisher:  outboxPublisher,
		orderConsumer:    orderConsumer,
		posSaleConsumer:  posSaleConsumer,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	// Start order events consumer for auto-consumption/release of stock reservations
	if a.orderConsumer != nil && a.events != nil {
		js, err := a.events.JetStream()
		if err != nil {
			a.log.Warn("jetstream unavailable, order events consumer not started", zap.Error(err))
		} else {
			go func() {
				if err := a.orderConsumer.Start(ctx, js); err != nil {
					a.log.Error("order events consumer stopped", zap.Error(err))
				}
			}()
			a.log.Info("order events consumer started")

			// Start POS sale events consumer
			if a.posSaleConsumer != nil {
				go func() {
					if err := a.posSaleConsumer.Start(ctx, js); err != nil {
						a.log.Error("pos sale events consumer stopped", zap.Error(err))
					}
				}()
				a.log.Info("pos sale events consumer started")
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
