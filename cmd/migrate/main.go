package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/migrate"
)

func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("POSTGRES_MIGRATE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_URL")
	}
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	// Every replica runs this binary on startup — without coordination, N pods launching at once
	// each run their own Schema.Create concurrently against the same tables. pos-api reproduced
	// exactly this in production 2026-07-26 (concurrent migrate attempts from multiple starting
	// replicas corrupted a nullable-then-backfill migration on a live table); standardizing on the
	// same fix here before inventory-api hits the same failure mode. A single physical connection
	// (MaxOpenConns=1) + a session-level advisory lock ensures only ONE pod across the whole
	// cluster ever executes the migration at a time; the rest block here until it finishes, then
	// find nothing pending and return immediately.
	//
	// Lock key 727271002 is inventory-api's own — distinct from pos-api's 727271001 and
	// treasury-api's 727271003. All services share ONE physical Postgres instance (see
	// postgres-topology-and-data-clearing memory), so reusing the same key across services would
	// make their migrate binaries block on each other for no reason.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	const migrationLockKey = 727271002
	if _, err := sqlDB.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		log.Fatalf("acquire migration lock: %v", err)
	}
	defer func() {
		if _, err := sqlDB.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			log.Printf("release migration lock: %v", err)
		}
	}()

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	// WithDropColumn/WithDropIndex must be explicit — without them, ent's live diff (schema.WithDir
	// here compares the CURRENT ent/schema/*.go structs against the live DB; there is no schema-
	// revisions table, it does not replay checked-in .sql files verbatim) silently SKIPS any
	// index/column removal a struct change implies. This exact gap already produced a real,
	// three-migration-long incident here: two stale item_pricings unique indexes survived silently
	// for weeks because a checked-in migration's DROP INDEX never actually ran, until they were
	// applied by hand via psql on 2026-08-04 (see 20260804220000_drop_stale_item_pricing_unique.sql
	// and the pos-notif-hub-fleet-relay-and-pricing-race-2026-08-12 memory file). Matches pos-api's
	// cmd/migrate/main.go, fixed there after its own 2026-07-26 outage for the identical reason.
	// Verified via a schema-only dump of production restored into a scratch DB before shipping
	// this: enabling these flags against the CURRENT production schema produces zero column/index
	// changes — no existing drift for them to act on, so this is safe to ship as-is; they only
	// change behavior for FUTURE migrations that drop something.
	if err := client.Schema.Create(ctx,
		schema.WithDir(migrate.Dir),
		schema.WithDropColumn(true),
		schema.WithDropIndex(true),
	); err != nil {
		log.Fatalf("schema create: %v", err)
	}

	fmt.Println("migrations completed successfully")
}
