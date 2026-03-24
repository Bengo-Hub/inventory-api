//go:build ignore

package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("POSTGRES_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/inventory?sslmode=disable"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		fmt.Println("connect error:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Println("ping error:", err)
		os.Exit(1)
	}

	// Ensure ent_dev schema exists (clean workspace for Atlas replay migrations)
	_, err = db.Exec("DROP SCHEMA IF EXISTS ent_dev CASCADE; CREATE SCHEMA ent_dev;")
	if err != nil {
		fmt.Println("schema error:", err)
		os.Exit(1)
	}
	fmt.Println("ent_dev schema created successfully (clean workspace for Atlas)")
}
