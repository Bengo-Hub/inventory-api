// Package testutil holds helpers shared by database-backed tests.
package testutil

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/inventory-service/internal/ent"
)

// SchemaClient returns an ent client on a fresh Postgres schema with the full inventory schema,
// dropped when the test ends. It skips the test when INVENTORY_TEST_DATABASE_URL is not set.
func SchemaClient(t *testing.T, prefix string) *ent.Client {
	t.Helper()
	url := os.Getenv("INVENTORY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INVENTORY_TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	// The item search indexes use trigram operator classes; the extension lives in public.
	if _, err := admin.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		t.Fatal(err)
	}
	schema := prefix + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	db, err := sql.Open("pgx", url+sep+"search_path="+schema+",public")
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return client
}
