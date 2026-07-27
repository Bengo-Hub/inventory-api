//go:build ignore

// verify_demo_category_hierarchy.go exercises items.Service.ListCategories — the exact
// function the GET /inventory/{tenant}/categories handler (ListCategories in
// internal/http/handlers/inventory.go) calls — and prints the resulting JSON, to confirm
// parent_id/depth/path came back correctly populated after fix_demo_category_hierarchy.go.
//
// Run from inventory-api root: go run ./scripts/verify_demo_category_hierarchy.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/tenant"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

func main() {
	_ = godotenv.Load()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()
	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()
	ctx := context.Background()

	tn, err := client.Tenant.Query().Where(tenant.Slug("codevertex-demo")).Only(ctx)
	if err != nil {
		log.Fatalf("find tenant: %v", err)
	}

	svc := items.NewService(client, zap.NewNop(), "http://localhost:4001")
	cats, err := svc.ListCategories(ctx, tn.ID)
	if err != nil {
		log.Fatalf("list categories: %v", err)
	}

	out, _ := json.MarshalIndent(map[string]any{"data": cats, "total": len(cats)}, "", "  ")
	fmt.Println(string(out))
}
