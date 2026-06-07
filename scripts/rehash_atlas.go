//go:build ignore

// rehash_atlas.go recomputes atlas.sum for the migrations directory using the Atlas Go library.
// Non-destructive (unlike fix_migrations.go) — it only rewrites the checksum file to match the
// current .sql migrations. Run from inventory-api root: go run ./scripts/rehash_atlas.go
package main

import (
	"fmt"
	"log"

	atlasmigrate "ariga.io/atlas/sql/migrate"
)

func main() {
	dir, err := atlasmigrate.NewLocalDir("internal/ent/migrate/migrations")
	if err != nil {
		log.Fatalf("failed opening migrations dir: %v", err)
	}
	sum, err := dir.Checksum()
	if err != nil {
		log.Fatalf("failed computing checksum: %v", err)
	}
	if err := atlasmigrate.WriteSumFile(dir, sum); err != nil {
		log.Fatalf("failed writing atlas.sum: %v", err)
	}
	fmt.Println("atlas.sum regenerated successfully")
}
