package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

const namespace = ""

// Config aggregates runtime configuration for the inventory service.
type Config struct {
	App           AppConfig
	HTTP          HTTPConfig
	Postgres      PostgresConfig
	Redis         RedisConfig
	Events        EventsConfig
	Telemetry     TelemetryConfig
	Auth          AuthConfig
	Media         MediaConfig
	Services      ServicesConfig
	Backup        BackupConfig
	EOL           EOLConfig
	ExpiryAlert   ExpiryAlertConfig
	Subscriptions SubscriptionsConfig
}

// EOLConfig controls the End-of-Life purge scheduler: items marked End-of-Life are hard-deleted
// (audit-safe) once their end_of_life_at is older than RetentionDays. Runs daily, tenant-generic,
// guarded by a Postgres advisory lock so only one replica performs the purge.
type EOLConfig struct {
	PurgeEnabled  bool `envconfig:"EOL_PURGE_ENABLED" default:"true"`
	RetentionDays int  `envconfig:"EOL_RETENTION_DAYS" default:"7"`
}

// ExpiryAlertConfig configures the pharmacy (DAWA) lot-expiry alert scan.
type ExpiryAlertConfig struct {
	ScheduleEnabled bool `envconfig:"EXPIRY_ALERT_SCHEDULE_ENABLED" default:"true"`
}

// SubscriptionsConfig holds configuration for the subscriptions S2S client used to gate
// cross-service NATS data sync by tenant entitlement. APIKey reuses the shared
// INTERNAL_SERVICE_KEY (same value other inventory S2S clients use).
type SubscriptionsConfig struct {
	ServiceURL     string        `envconfig:"SUBSCRIPTION_BASE_URL" default:"https://pricingapi.codevertexafrica.com"`
	RequestTimeout time.Duration `envconfig:"SUBSCRIPTIONS_REQUEST_TIMEOUT" default:"10s"`
	APIKey         string        `envconfig:"INTERNAL_SERVICE_KEY" default:""`
}

// BackupConfig controls the tenant-scoped backup scheduler + retention churn. Artifacts are
// written to a local directory (Dir) — typically a PVC, separate from the public media path.
type BackupConfig struct {
	Dir             string `envconfig:"BACKUP_DIR" default:"/app/backups/inventory"`
	ScheduleEnabled bool   `envconfig:"BACKUP_SCHEDULE_ENABLED" default:"true"`
	ScheduleHour    int    `envconfig:"BACKUP_SCHEDULE_HOUR" default:"2"`
	RetentionDays   int    `envconfig:"BACKUP_RETENTION_DAYS" default:"4"`
}

type MediaConfig struct {
	Root    string `envconfig:"MEDIA_ROOT" default:"./media"`
	URLBase string `envconfig:"MEDIA_URL_BASE" default:""`
}

type AppConfig struct {
	Name    string `envconfig:"APP_NAME" default:"inventory-service"`
	Env     string `envconfig:"APP_ENV" default:"development"`
	Region  string `envconfig:"APP_REGION" default:"africa-east-1"`
	Version string `envconfig:"APP_VERSION" default:"0.1.0"`
}

type HTTPConfig struct {
	Host           string        `envconfig:"HTTP_HOST" default:"0.0.0.0"`
	Port           int           `envconfig:"HTTP_PORT" default:"4001"`
	ReadTimeout    time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"20s"`
	WriteTimeout   time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"20s"`
	IdleTimeout    time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"90s"`
	TLSCertFile    string        `envconfig:"TLS_CERT_FILE"`
	TLSKeyFile     string        `envconfig:"TLS_KEY_FILE"`
	AllowedOrigins []string      `envconfig:"HTTP_ALLOWED_ORIGINS" default:"https://pos.codevertexafrica.com,https://ordering.codevertexafrica.com,https://accounts.codevertexafrica.com,https://theurbanloftcafe.com,https://inventory.codevertexafrica.com"`
}

type PostgresConfig struct {
	URL string `envconfig:"POSTGRES_URL" default:"postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"`
	// ReadOnlyURL points at a read replica (through pgbouncer's inventory_ro alias in prod) for
	// the handful of heavy, staleness-tolerant read endpoints wired to use it (ListItems' catalog
	// search/list today — see app.go). Empty (the default everywhere this isn't explicitly
	// configured, incl. local dev) falls back to the primary client — zero behavior change.
	ReadOnlyURL              string        `envconfig:"POSTGRES_READONLY_URL"`
	MaxOpenConns             int           `envconfig:"POSTGRES_MAX_OPEN_CONNS" default:"5"`
	MaxIdleConns             int           `envconfig:"POSTGRES_MAX_IDLE_CONNS" default:"3"`
	ConnMaxLifetime          time.Duration `envconfig:"POSTGRES_CONN_MAX_LIFETIME" default:"5m"`
	StatementTimeout         time.Duration `envconfig:"POSTGRES_STATEMENT_TIMEOUT" default:"30s"`
	IdleInTransactionTimeout time.Duration `envconfig:"POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT" default:"60s"`
	RunMigrations            bool          `envconfig:"POSTGRES_RUN_MIGRATIONS" default:"false"`
}

type RedisConfig struct {
	Addr        string        `envconfig:"REDIS_ADDR" default:"localhost:6380"`
	Username    string        `envconfig:"REDIS_USERNAME"`
	Password    string        `envconfig:"REDIS_PASSWORD"`
	DB          int           `envconfig:"REDIS_DB" default:"0"`
	TLSRequired bool          `envconfig:"REDIS_TLS_REQUIRED" default:"false"`
	DialTimeout time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
}

type EventsConfig struct {
	Bus              string        `envconfig:"EVENT_BUS" default:"nats"`
	NATSURL          string        `envconfig:"EVENTS_NATS_URL" default:"nats://localhost:4222"`
	StreamName       string        `envconfig:"NATS_STREAM" default:"inventory"`
	DeliverGroup     string        `envconfig:"NATS_DELIVER_GROUP" default:"inventory-workers"`
	DeadLetterJet    string        `envconfig:"NATS_DLQ_STREAM" default:"inventory-dlq"`
	OutboxEnabled    bool          `envconfig:"OUTBOX_ENABLED" default:"true"`
	OutboxBatchSize  int           `envconfig:"OUTBOX_BATCH_SIZE" default:"100"`
	OutboxPollPeriod time.Duration `envconfig:"OUTBOX_POLL_PERIOD" default:"5s"`
}

type TelemetryConfig struct {
	OTLPEndpoint string `envconfig:"OTLP_ENDPOINT"`
	MetricsURL   string `envconfig:"METRICS_ENDPOINT"`
	TracingURL   string `envconfig:"TRACING_ENDPOINT"`
}

type ServicesConfig struct {
	OrderingURL string `envconfig:"ORDERING_SERVICE_URL" default:"https://orderingapi.codevertexafrica.com"`
	// TreasuryURL is the treasury-api base URL — source of truth for tax codes/rates (S2S).
	TreasuryURL string `envconfig:"TREASURY_SERVICE_URL" default:"https://booksapi.codevertexafrica.com"`
	// POSURL is the pos-api base URL — source of POS units-sold (S2S) for menu-engineering/variance.
	POSURL string `envconfig:"POS_SERVICE_URL" default:"https://posapi.codevertexafrica.com"`
}

type AuthConfig struct {
	// Auth Service SSO (JWT) integration
	ServiceURL          string        `envconfig:"AUTH_SERVICE_URL" default:"https://sso.codevertexafrica.com"`
	Issuer              string        `envconfig:"AUTH_ISSUER" default:"https://sso.codevertexafrica.com"`
	Audience            string        `envconfig:"AUTH_AUDIENCE" default:"codevertex"`
	JWKSUrl             string        `envconfig:"AUTH_JWKS_URL" default:"https://sso.codevertexafrica.com/api/v1/.well-known/jwks.json"`
	JWKSCacheTTL        time.Duration `envconfig:"AUTH_JWKS_CACHE_TTL" default:"3600s"`
	JWKSRefreshInterval time.Duration `envconfig:"AUTH_JWKS_REFRESH_INTERVAL" default:"300s"`
	EnableAPIKeyAuth    bool          `envconfig:"AUTH_ENABLE_API_KEY_AUTH" default:"true"`
	APIKey              string        `envconfig:"INTERNAL_SERVICE_KEY" default:""`
	// TerminalJWTSecret signs desk/kiosk PIN (terminal) JWTs. Falls back to INTERNAL_SERVICE_KEY
	// (via terminalJWTSecret in app.go) when unset, mirroring pos-api / library-api.
	TerminalJWTSecret string `envconfig:"TERMINAL_JWT_SECRET" default:""`
}

// Load gathers configuration from environment variables and optional .env files.
func Load() (*Config, error) {
	_ = godotenv.Load()

	var cfg Config
	if err := envconfig.Process(namespace, &cfg); err != nil {
		return nil, fmt.Errorf("config: failed to load environment variables: %w", err)
	}

	return &cfg, nil
}
