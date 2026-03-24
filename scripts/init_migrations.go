//go:build ignore

// init_migrations.go initializes a clean migration directory with atlas.sum.
// Run: go run scripts/init_migrations.go
package main

import (
	"fmt"
	"os"

	atlasmigrate "ariga.io/atlas/sql/migrate"
)

func main() {
	dir := "internal/ent/migrate/migrations"

	// Remove all existing files
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		os.Remove(dir + "/" + e.Name())
	}

	// Create a local dir and write a properly checksummed atlas.sum
	localDir, err := atlasmigrate.NewLocalDir(dir)
	if err != nil {
		fmt.Println("Error creating local dir:", err)
		os.Exit(1)
	}

	// Write a placeholder SQL file
	err = localDir.WriteFile("00000000000000_placeholder.sql", []byte("-- placeholder for embed\n"))
	if err != nil {
		fmt.Println("Error writing placeholder:", err)
		os.Exit(1)
	}

	// Compute and write atlas.sum (checksum file)
	sum, err := localDir.Checksum()
	if err != nil {
		fmt.Println("Error computing checksum:", err)
		os.Exit(1)
	}
	err = atlasmigrate.WriteSumFile(localDir, sum)
	if err != nil {
		fmt.Println("Error writing atlas.sum:", err)
		os.Exit(1)
	}

	fmt.Println("Migration directory initialized with valid atlas.sum")
}
