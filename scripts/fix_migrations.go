//go:build ignore

// fix_migrations.go clears the inventory database and generates a fresh Atlas initial migration.
// Run from inventory-api root: go run ./scripts/fix_migrations.go
// Requires: POSTGRES_URL (default: postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable)
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dbURL := os.Getenv("POSTGRES_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get cwd: %v", err)
	}
	migrationsDir := filepath.Join(cwd, "internal", "ent", "migrate", "migrations")

	// 1. Clear database
	log.Println("Connecting to database...")
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database (is Postgres running?): %v", err)
	}

	log.Println("Dropping and recreating schemas (public, ent_dev)...")
	_, err = db.Exec(`
		DROP SCHEMA IF EXISTS public CASCADE;
		DROP SCHEMA IF EXISTS ent_dev CASCADE;
		CREATE SCHEMA public;
		GRANT ALL ON SCHEMA public TO postgres;
		GRANT ALL ON SCHEMA public TO public;
	`)
	if err != nil {
		log.Fatalf("Failed to clear database: %v", err)
	}
	db.Close()
	log.Println("✓ Database cleared")

	// 2. Clean migration directory and initialize with valid atlas.sum
	log.Println("Cleaning migrations directory...")
	files, _ := os.ReadDir(migrationsDir)
	for _, f := range files {
		if !f.IsDir() && (strings.HasSuffix(f.Name(), ".sql") || f.Name() == "atlas.sum") {
			os.Remove(filepath.Join(migrationsDir, f.Name()))
		}
	}

	localDir, err := atlasmigrate.NewLocalDir(migrationsDir)
	if err != nil {
		log.Fatalf("Failed creating atlas dir: %v", err)
	}
	err = localDir.WriteFile("00000000000000_placeholder.sql", []byte("-- placeholder for embed\n"))
	if err != nil {
		log.Fatalf("Failed writing placeholder: %v", err)
	}
	sum, err := localDir.Checksum()
	if err != nil {
		log.Fatalf("Failed computing checksum: %v", err)
	}
	err = atlasmigrate.WriteSumFile(localDir, sum)
	if err != nil {
		log.Fatalf("Failed writing atlas.sum: %v", err)
	}
	log.Println("✓ Migrations directory initialized with valid atlas.sum")

	// 3. Generate fresh initial migration
	log.Println("Generating fresh Atlas initial migration...")
	cmd := exec.Command("go", "run", "-mod=mod", "internal/ent/migrate/main.go", "initial_schema")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "POSTGRES_URL="+dbURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to generate migration: %v", err)
	}
	log.Println("✓ New initial migration generated")

	// 4. Remove placeholder
	os.Remove(filepath.Join(migrationsDir, "00000000000000_placeholder.sql"))

	// 5. Rewrite atlas.sum
	localDir2, _ := atlasmigrate.NewLocalDir(migrationsDir)
	sum2, err := localDir2.Checksum()
	if err != nil {
		log.Printf("Warning: could not recompute checksum: %v", err)
	} else {
		atlasmigrate.WriteSumFile(localDir2, sum2)
		log.Println("✓ atlas.sum updated")
	}

	fmt.Println("\nDone! Run 'go run ./cmd/api/' to apply migrations and start the API.")
}
